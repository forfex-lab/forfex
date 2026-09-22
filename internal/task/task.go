// Package task turns a free-form MCP tool call into a declared unit of
// work — the task-shaped tool wrappers CGTRU-18 names.
//
// internal/mcp can call any tool on any worker with any arguments, and
// the CALLER states the provenance. That last part is the problem this
// package exists for. `forfex call -tool x -provenance published` is an
// assertion by whoever typed it, checked by nothing, and Rule 4 then
// faithfully enforces a label the caller chose. The boundary is real;
// what crosses it is self-reported.
//
// A Task closes that. It declares every argument it accepts and the
// provenance of each one, and Run DERIVES the payload's provenance from
// the inputs actually supplied rather than accepting a claim. The
// declaration lives with the task definition, which is reviewed once,
// instead of with the call site, which is written every time.
//
// An argument the task did not declare is refused before dispatch —
// not for tidiness, but because an undeclared argument carries no
// declared provenance, and the derivation would then be a guess about
// the one value that decides whether the payload may leave. A closed
// argument surface is what makes the derived label mean anything.
//
// # What this package does not change
//
// The content arm of Rule 4 is untouched and independent. A nucleotide
// run is found by privacy.Check on the serialised body exactly as
// before, whatever a task declares — a task marking every input
// `published` does not let a sequence through. This package improves
// the PROVENANCE arm only, and improves it from "asserted" to
// "derived from a reviewed declaration". Neither arm is a control
// against a caller who is trying to get around it.
//
// Provenance is declared per top-level argument. A nested object is
// covered by the declaration on its root key, so a task that accepts a
// free-form config map is making one claim about everything inside it.
// Prefer narrow, named inputs for that reason.
package task

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/forfex-lab/forfex/internal/bus"
	"github.com/forfex-lab/forfex/internal/mcp"
	"github.com/forfex-lab/forfex/internal/privacy"
)

var (
	// ErrUndeclaredArgument means the caller passed an argument the
	// task does not declare. Nothing was dispatched.
	ErrUndeclaredArgument = errors.New("task: undeclared argument")
	// ErrMissingArgument means a required input was absent.
	ErrMissingArgument = errors.New("task: missing required argument")
	// ErrInvalidTask means the definition itself is malformed.
	ErrInvalidTask = errors.New("task: invalid definition")
	// ErrUnknownTask means the name is not in the registry.
	ErrUnknownTask = errors.New("task: unknown task")
)

// Input is one declared argument of a Task.
//
// Provenance is the field that matters. Its zero value is
// privacy.Unknown, which privacy treats as unpublished, so an Input
// literal that omits it costs a refused external dispatch rather than
// an unlabelled payload — the same direction the privacy and bus
// packages chose for their own zero values.
type Input struct {
	Name       string
	Required   bool
	Provenance privacy.Provenance
	Summary    string
}

// Task is one named unit of work: one tool on one worker, with a
// closed set of declared arguments.
type Task struct {
	// Name is how the orchestrator refers to this task. It is not the
	// tool name and should not be: the task is the stable surface, and
	// the tool behind it may be replaced by a different container.
	Name    string
	Summary string

	// Worker is the bus worker this task runs on. Whether that worker
	// is local or external is the bus's business, not the task's — a
	// task declares what it sends, never where it may go.
	Worker string
	Tool   string

	Inputs []Input

	// Baseline is the provenance of the call itself: the tool name and
	// the shape of the request, before any argument is considered.
	//
	// It is a FLOOR on restrictiveness, never a ceiling. Effective
	// takes the most restricted of the baseline and every supplied
	// input, so a baseline of Published cannot loosen an input that
	// declares something stricter. The zero value is Unknown, so a task
	// that never thinks about this is not externally routable.
	Baseline privacy.Provenance
}

// Validate reports whether the definition is usable. Run and
// Registry.Register both call it, so a malformed task fails where it is
// written rather than on the first call.
func (t Task) Validate() error {
	var problems []string
	if t.Name == "" {
		problems = append(problems, "name is empty")
	}
	if t.Worker == "" {
		problems = append(problems, "worker is empty")
	}
	if t.Tool == "" {
		problems = append(problems, "tool is empty")
	}

	seen := make(map[string]bool, len(t.Inputs))
	for i, in := range t.Inputs {
		switch {
		case in.Name == "":
			problems = append(problems, fmt.Sprintf("input %d has no name", i))
		case seen[in.Name]:
			// Two declarations of one name would make the derived
			// provenance depend on slice order, which is the one thing
			// it must not depend on.
			problems = append(problems, fmt.Sprintf("input %q is declared twice", in.Name))
		default:
			seen[in.Name] = true
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s: %s", ErrInvalidTask, t.Name, strings.Join(problems, "; "))
}

// CheckArgs enforces the closed argument surface.
//
// Argument NAMES appear in the errors; argument VALUES never do. The
// privacy package keeps its refusal reasons free of payload content for
// the same reason, and a task refusal is one step earlier in the same
// path.
func (t Task) CheckArgs(args map[string]any) error {
	declared := make(map[string]Input, len(t.Inputs))
	for _, in := range t.Inputs {
		declared[in.Name] = in
	}

	var undeclared []string
	for k := range args {
		if _, ok := declared[k]; !ok {
			undeclared = append(undeclared, k)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return fmt.Errorf("%w: %s does not accept %s (accepts: %s)",
			ErrUndeclaredArgument, t.Name,
			quoteJoin(undeclared), quoteJoin(t.InputNames()))
	}

	var missing []string
	for _, in := range t.Inputs {
		if !in.Required {
			continue
		}
		if _, ok := args[in.Name]; !ok {
			missing = append(missing, in.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: %s requires %s",
			ErrMissingArgument, t.Name, quoteJoin(missing))
	}
	return nil
}

// InputNames returns the declared argument names, sorted.
func (t Task) InputNames() []string {
	names := make([]string, 0, len(t.Inputs))
	for _, in := range t.Inputs {
		names = append(names, in.Name)
	}
	sort.Strings(names)
	return names
}

// Effective returns the provenance of one call and what set it: the
// most restricted of the task's baseline and the provenance of every
// argument actually supplied. The second return is the input name
// responsible, or "" when the baseline alone decided.
//
// It assumes args have been through CheckArgs — an undeclared key has
// no declaration to contribute, so calling this on unvalidated args
// would ignore it. Run validates first, and nothing in this package
// dispatches without Run.
func (t Task) Effective(args map[string]any) (privacy.Provenance, string) {
	worst, from := t.Baseline, ""
	for _, in := range t.Inputs {
		if _, supplied := args[in.Name]; !supplied {
			continue
		}
		if rank(in.Provenance) > rank(worst) {
			worst, from = in.Provenance, in.Name
		}
	}
	return worst, from
}

// maxRank is above every named provenance, so an unrecognised value
// sorts as the most restricted.
const maxRank = 4

// rank orders provenance by how restricted it is.
//
// privacy.Published is the only value that may reach an external
// destination, so it is the only rank 0 — and everything else,
// INCLUDING a value this function has never seen, ranks above it. A
// provenance constant added to the privacy package later therefore
// arrives here as maximally restricted rather than as a hole that has
// to be noticed.
func rank(p privacy.Provenance) int {
	switch p {
	case privacy.Published:
		return 0
	case privacy.Unknown:
		return 1
	case privacy.Unpublished:
		return 2
	case privacy.ELabFTW:
		return 3
	}
	return maxRank
}

// Run validates the definition, enforces the argument surface, derives
// the provenance and dispatches through the bus.
//
// It does not choose a destination and cannot override one: the bus
// resolves the worker, and privacy.Check decides. A task that derives
// an unpublished provenance and names an external worker is refused by
// the boundary, not by this function.
func Run(ctx context.Context, b *bus.Bus, t Task, args map[string]any) (mcp.ToolResult, error) {
	if err := t.Validate(); err != nil {
		return mcp.ToolResult{}, err
	}
	if err := t.CheckArgs(args); err != nil {
		return mcp.ToolResult{}, err
	}
	prov, _ := t.Effective(args)
	return mcp.Dispatch(ctx, b, t.Worker, mcp.ToolCall{
		Tool:      t.Tool,
		Arguments: args,
	}, prov)
}

// quoteJoin renders a list of names for an error message.
//
// strconv.Quote rather than %q on the slice: these names can come from
// the caller, and quoting each one escapes control characters instead
// of letting them into a terminal or a log line.
func quoteJoin(ss []string) string {
	if len(ss) == 0 {
		return "(none)"
	}
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}
