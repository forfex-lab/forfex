package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/forfex-lab/forfex/internal/mcp"
)

// ErrNoSchema means the server published no inputSchema for a tool, so
// the declaration could not be checked against anything.
//
// It is an error rather than a silent pass on purpose. A verifier that
// returns "clean" when it looked at nothing is the failure this project
// has already been bitten by: a gate that was documented, believed, and
// never actually ran.
var ErrNoSchema = errors.New("task: tool publishes no inputSchema")

// Verify compares a task's declared inputs against the schema its
// server publishes for the tool.
//
// This is a WIRING-TIME contract check, not per-call argument
// validation. The decision to let the server validate arguments stands:
// re-implementing JSON Schema here to reject calls the server would
// reject anyway buys nothing. What this catches is different and not
// the server's job — our declarations drifting away from the tool they
// describe. A misspelled input name is an argument that is never
// supplied and therefore a provenance that is never contributed, which
// is a labelling failure rather than a call failure, and it would not
// show up as a broken call.
//
// Returns nil when the declaration and the schema agree, ErrNoSchema
// when there was nothing to compare, and an error listing every
// disagreement otherwise.
func Verify(t Task, tool mcp.Tool) error {
	if len(tool.InputSchema) == 0 || string(tool.InputSchema) == "null" {
		return fmt.Errorf("%w: %s", ErrNoSchema, tool.Name)
	}

	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		return fmt.Errorf("task: %s: inputSchema for %s is not a JSON object: %w",
			t.Name, tool.Name, err)
	}

	declared := make(map[string]Input, len(t.Inputs))
	for _, in := range t.Inputs {
		declared[in.Name] = in
	}

	var problems []string

	for _, name := range t.InputNames() {
		if _, ok := schema.Properties[name]; !ok {
			problems = append(problems,
				fmt.Sprintf("declares input %q, which %s does not accept", name, tool.Name))
		}
	}

	required := append([]string(nil), schema.Required...)
	sort.Strings(required)
	for _, name := range required {
		in, ok := declared[name]
		switch {
		case !ok:
			problems = append(problems,
				fmt.Sprintf("%s requires %q, which the task does not declare", tool.Name, name))
		case !in.Required:
			problems = append(problems,
				fmt.Sprintf("%s requires %q, which the task declares optional", tool.Name, name))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("task %s vs tool %s: %s", t.Name, tool.Name, strings.Join(problems, "; "))
}

// Report is the result of checking a catalogue against a live server.
//
// The three lists are kept apart so a run that verified nothing cannot
// read as a run that found nothing wrong. OK() is deliberately not the
// whole story: a report with no problems and no Verified entries means
// the check did not happen.
type Report struct {
	// Verified names the tasks whose inputs were compared against a
	// published schema and agreed with it.
	Verified []string
	// Unverifiable names the tasks that could not be checked, with the
	// reason — one line each.
	Unverifiable []string
	// Problems names the disagreements — one line each.
	Problems []string
}

// OK reports whether anything disagreed. It says nothing about how much
// was checked; read Verified and Unverifiable for that.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Summary is one line, and always states all three counts so a vacuous
// run is visible in the output rather than inferable from it.
func (r Report) Summary() string {
	return fmt.Sprintf("%d verified, %d unverifiable, %d problems",
		len(r.Verified), len(r.Unverifiable), len(r.Problems))
}

// VerifyAgainst checks every task in a catalogue against the tool list
// one server published.
//
// Tasks whose Worker is not this server are skipped entirely and appear
// nowhere in the report: a catalogue spans several containers, and a
// task for a different worker is not evidence of anything here.
func VerifyAgainst(tasks []Task, worker string, tools []mcp.Tool) Report {
	byName := make(map[string]mcp.Tool, len(tools))
	for _, tl := range tools {
		byName[tl.Name] = tl
	}

	var rep Report
	for _, t := range tasks {
		if t.Worker != worker {
			continue
		}
		tool, ok := byName[t.Tool]
		if !ok {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("task %s calls %q, which %s does not offer", t.Name, t.Tool, worker))
			continue
		}
		err := Verify(t, tool)
		switch {
		case errors.Is(err, ErrNoSchema):
			rep.Unverifiable = append(rep.Unverifiable,
				fmt.Sprintf("task %s: %v", t.Name, err))
		case err != nil:
			rep.Problems = append(rep.Problems, err.Error())
		default:
			rep.Verified = append(rep.Verified, t.Name)
		}
	}
	return rep
}
