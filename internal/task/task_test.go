package task_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/forfex-lab/forfex/internal/bus"
	"github.com/forfex-lab/forfex/internal/mcp"
	"github.com/forfex-lab/forfex/internal/privacy"
	"github.com/forfex-lab/forfex/internal/task"
)

// recorder is a bus.Transport that remembers every body it was handed.
//
// Most of the assertions below are about what was NOT sent. An error
// return alone does not prove that: a function can dispatch, fail, and
// report an error that looks identical to a refusal. Counting bodies is
// the difference between "it said no" and "nothing left".
type recorder struct {
	mu     sync.Mutex
	bodies []string
}

func (r *recorder) Send(_ context.Context, _ bus.Worker, body string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, body)
	return `{"content":[{"type":"text","text":"ok"}]}`, nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *recorder) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return ""
	}
	return r.bodies[len(r.bodies)-1]
}

// newBus wires a recorder to one worker at the given destination.
func newBus(t *testing.T, dest privacy.Destination) (*bus.Bus, *recorder) {
	t.Helper()
	rec := &recorder{}
	b := bus.New(rec, nil)
	if err := b.Register(bus.Worker{Name: "w", Destination: dest}); err != nil {
		t.Fatalf("registering worker: %v", err)
	}
	return b, rec
}

// lookupTask is the fixture: two inputs with different provenance, so
// which one is supplied changes where the call may go.
func lookupTask() task.Task {
	return task.Task{
		Name:     "demo.lookup",
		Worker:   "w",
		Tool:     "lookup",
		Baseline: privacy.Published,
		Inputs: []task.Input{
			{Name: "doi", Required: true, Provenance: privacy.Published},
			{Name: "notebook_entry", Provenance: privacy.ELabFTW},
		},
	}
}

func TestUndeclaredArgumentIsRefusedAndNothingIsSent(t *testing.T) {
	b, rec := newBus(t, privacy.Local)

	_, err := task.Run(context.Background(), b, lookupTask(), map[string]any{
		"doi":      "10.1016/j.ymthe.2021.10.026",
		"smuggled": "anything at all",
	})
	if !errors.Is(err, task.ErrUndeclaredArgument) {
		t.Fatalf("want ErrUndeclaredArgument, got %v", err)
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("an undeclared argument reached the transport: %d bodies sent", n)
	}
	// The message has to name the offending key, or the caller cannot
	// act on it.
	if !strings.Contains(err.Error(), `"smuggled"`) {
		t.Errorf("error does not name the undeclared argument: %v", err)
	}
}

func TestMissingRequiredArgumentIsRefusedAndNothingIsSent(t *testing.T) {
	b, rec := newBus(t, privacy.Local)

	_, err := task.Run(context.Background(), b, lookupTask(), map[string]any{})
	if !errors.Is(err, task.ErrMissingArgument) {
		t.Fatalf("want ErrMissingArgument, got %v", err)
	}
	if n := rec.count(); n != 0 {
		t.Fatalf("a call with a missing required argument was sent: %d bodies", n)
	}
}

// TestCallerCannotClaimPublishedThroughATask is the reason this package
// exists, and it asserts the contrast rather than the refusal alone:
// the SAME arguments go through when the caller states the provenance
// directly, and are refused when a task derives it.
func TestCallerCannotClaimPublishedThroughATask(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{
		"doi":            "10.1016/j.ymthe.2021.10.026",
		"notebook_entry": "experiment 7, day 4 counts",
	}

	// Half one: the existing path. The caller asserts `published` and
	// the boundary believes it, because there is nothing to check it
	// against.
	b, rec := newBus(t, privacy.External)
	if _, err := mcp.Dispatch(ctx, b, "w",
		mcp.ToolCall{Tool: "lookup", Arguments: args}, privacy.Published); err != nil {
		t.Fatalf("direct dispatch with an asserted provenance: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("expected the asserted-provenance call to be sent, got %d bodies", rec.count())
	}

	// Half two: the same arguments, on the same worker, through a task
	// that declares where `notebook_entry` comes from.
	_, err := task.Run(ctx, b, lookupTask(), args)
	if !errors.Is(err, bus.ErrRefused) {
		t.Fatalf("want bus.ErrRefused from the task path, got %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("the task path sent something: %d bodies total, want 1", rec.count())
	}
	if !strings.Contains(err.Error(), "elabftw") {
		t.Errorf("refusal does not name the provenance that caused it: %v", err)
	}
}

func TestOmittingTheRestrictedInputRestoresExternalRouting(t *testing.T) {
	b, rec := newBus(t, privacy.External)

	// Same task, same worker — only the restricted input is absent.
	res, err := task.Run(context.Background(), b, lookupTask(), map[string]any{
		"doi": "10.1016/j.ymthe.2021.10.026",
	})
	if err != nil {
		t.Fatalf("published-only call was refused: %v", err)
	}
	if res.Text() != "ok" {
		t.Errorf("unexpected result text %q", res.Text())
	}
	if rec.count() != 1 {
		t.Fatalf("want 1 body sent, got %d", rec.count())
	}
	if !strings.Contains(rec.last(), `"tool":"lookup"`) {
		t.Errorf("body does not carry the tool name: %s", rec.last())
	}
}

func TestRestrictedProvenanceStillReachesALocalWorker(t *testing.T) {
	b, rec := newBus(t, privacy.Local)

	if _, err := task.Run(context.Background(), b, lookupTask(), map[string]any{
		"doi":            "10.1016/j.ymthe.2021.10.026",
		"notebook_entry": "experiment 7, day 4 counts",
	}); err != nil {
		t.Fatalf("local worker refused restricted provenance: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("want 1 body sent to the local worker, got %d", rec.count())
	}
}

// TestContentArmIsUnchanged pins the claim made in the package comment:
// declaring an input `published` does not exempt it from the nucleotide
// filter. The two arms of Rule 4 are independent, and a task can only
// tighten the provenance arm.
func TestContentArmIsUnchanged(t *testing.T) {
	b, rec := newBus(t, privacy.External)

	seq := strings.Repeat("ACGT", 6) // 24 characters, over MinRun
	_, err := task.Run(context.Background(), b, task.Task{
		Name:     "demo.publish",
		Worker:   "w",
		Tool:     "lookup",
		Baseline: privacy.Published,
		Inputs: []task.Input{
			{Name: "doi", Required: true, Provenance: privacy.Published},
		},
	}, map[string]any{"doi": seq})

	if !errors.Is(err, bus.ErrRefused) {
		t.Fatalf("want bus.ErrRefused for a nucleotide run, got %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("a nucleotide run reached the transport: %d bodies", rec.count())
	}
	if strings.Contains(err.Error(), seq) {
		t.Error("the refusal echoed the sequence it just blocked")
	}
}

func TestEffective(t *testing.T) {
	tk := lookupTask()

	cases := []struct {
		name     string
		args     map[string]any
		wantProv privacy.Provenance
		wantFrom string
	}{
		{
			name:     "baseline alone when nothing restricted is supplied",
			args:     map[string]any{"doi": "x"},
			wantProv: privacy.Published,
			wantFrom: "",
		},
		{
			name:     "the restricted input decides and is named",
			args:     map[string]any{"doi": "x", "notebook_entry": "y"},
			wantProv: privacy.ELabFTW,
			wantFrom: "notebook_entry",
		},
		{
			name:     "a declared input that was not supplied contributes nothing",
			args:     map[string]any{},
			wantProv: privacy.Published,
			wantFrom: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, from := tk.Effective(c.args)
			if got != c.wantProv || from != c.wantFrom {
				t.Fatalf("Effective = (%q, %q), want (%q, %q)",
					got, from, c.wantProv, c.wantFrom)
			}
		})
	}
}

// TestBaselineCannotLoosenAnInput walks every provenance the privacy
// package names, plus one it does not, and asserts the property the
// whole derivation rests on: if any contributing provenance would be
// refused externally, the derived one is refused too.
//
// Written as a property rather than three examples because the failure
// it guards against is a new provenance constant being added to the
// privacy package and ranking below Published by accident.
func TestBaselineCannotLoosenAnInput(t *testing.T) {
	all := []privacy.Provenance{
		privacy.Published,
		privacy.Unknown,
		privacy.Unpublished,
		privacy.ELabFTW,
		privacy.Provenance("something-invented-later"),
	}

	for _, baseline := range all {
		for _, input := range all {
			tk := task.Task{
				Name:     "t",
				Worker:   "w",
				Tool:     "tool",
				Baseline: baseline,
				Inputs:   []task.Input{{Name: "a", Provenance: input}},
			}
			got, _ := tk.Effective(map[string]any{"a": 1})

			derivedOK := privacy.Check(privacy.Payload{Provenance: got}, privacy.External).Allowed
			baselineOK := privacy.Check(privacy.Payload{Provenance: baseline}, privacy.External).Allowed
			inputOK := privacy.Check(privacy.Payload{Provenance: input}, privacy.External).Allowed

			if derivedOK && !(baselineOK && inputOK) {
				t.Errorf("baseline %q + input %q derived %q, which is externally allowed "+
					"although a contributor is not", baseline, input, got)
			}
		}
	}
}

func TestValidateRejectsMalformedDefinitions(t *testing.T) {
	cases := []struct {
		name string
		task task.Task
		want string
	}{
		{"no name", task.Task{Worker: "w", Tool: "t"}, "name is empty"},
		{"no worker", task.Task{Name: "n", Tool: "t"}, "worker is empty"},
		{"no tool", task.Task{Name: "n", Worker: "w"}, "tool is empty"},
		{
			name: "duplicate input name",
			task: task.Task{Name: "n", Worker: "w", Tool: "t", Inputs: []task.Input{
				{Name: "a", Provenance: privacy.Published},
				{Name: "a", Provenance: privacy.ELabFTW},
			}},
			want: `"a" is declared twice`,
		},
		{
			name: "unnamed input",
			task: task.Task{Name: "n", Worker: "w", Tool: "t", Inputs: []task.Input{{}}},
			want: "input 0 has no name",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.task.Validate()
			if !errors.Is(err, task.ErrInvalidTask) {
				t.Fatalf("want ErrInvalidTask, got %v", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// A duplicate input name is the one malformed definition that could
// otherwise dispatch, so Run must reject it rather than trusting
// Register to have been called.
func TestRunRejectsAnInvalidTaskWithoutSending(t *testing.T) {
	b, rec := newBus(t, privacy.External)

	_, err := task.Run(context.Background(), b, task.Task{
		Name: "dup", Worker: "w", Tool: "tool",
		Inputs: []task.Input{
			{Name: "a", Provenance: privacy.ELabFTW},
			{Name: "a", Provenance: privacy.Published},
		},
	}, map[string]any{"a": 1})

	if !errors.Is(err, task.ErrInvalidTask) {
		t.Fatalf("want ErrInvalidTask, got %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("an invalid task dispatched: %d bodies", rec.count())
	}
}

func TestRegistry(t *testing.T) {
	r := task.NewRegistry()

	if err := r.Register(lookupTask()); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := r.Register(lookupTask()); err == nil {
		t.Error("registry replaced an existing task silently")
	}
	if err := r.Register(task.Task{Name: "broken"}); !errors.Is(err, task.ErrInvalidTask) {
		t.Errorf("registry accepted an invalid task: %v", err)
	}

	if got := r.Names(); len(got) != 1 || got[0] != "demo.lookup" {
		t.Errorf("Names() = %v", got)
	}
	if _, ok := r.Lookup("demo.lookup"); !ok {
		t.Error("Lookup missed a registered task")
	}

	b, rec := newBus(t, privacy.Local)
	if _, err := r.Run(context.Background(), b, "nope", nil); !errors.Is(err, task.ErrUnknownTask) {
		t.Errorf("want ErrUnknownTask, got %v", err)
	}
	if rec.count() != 0 {
		t.Errorf("an unknown task dispatched: %d bodies", rec.count())
	}
	if _, err := r.Run(context.Background(), b, "demo.lookup", map[string]any{"doi": "x"}); err != nil {
		t.Errorf("registry Run: %v", err)
	}
}
