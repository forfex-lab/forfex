package task_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/forfex-lab/forfex/internal/mcp"
	"github.com/forfex-lab/forfex/internal/privacy"
	"github.com/forfex-lab/forfex/internal/task"
)

func tool(name, schema string) mcp.Tool {
	return mcp.Tool{Name: name, InputSchema: json.RawMessage(schema)}
}

const lookupSchema = `{
  "type": "object",
  "properties": {"doi": {"type": "string"}, "notebook_entry": {"type": "string"}},
  "required": ["doi"]
}`

func TestVerifyAcceptsAnAgreeingDeclaration(t *testing.T) {
	if err := task.Verify(lookupTask(), tool("lookup", lookupSchema)); err != nil {
		t.Fatalf("clean declaration reported a problem: %v", err)
	}
}

func TestVerifyCatchesDrift(t *testing.T) {
	cases := []struct {
		name string
		task task.Task
		want string
	}{
		{
			name: "declares an input the tool does not accept",
			task: task.Task{Name: "t", Worker: "w", Tool: "lookup", Inputs: []task.Input{
				{Name: "doi", Required: true, Provenance: privacy.Published},
				{Name: "doii", Provenance: privacy.Published},
			}},
			want: `declares input "doii"`,
		},
		{
			name: "does not declare something the tool requires",
			task: task.Task{Name: "t", Worker: "w", Tool: "lookup"},
			want: `requires "doi", which the task does not declare`,
		},
		{
			name: "declares a required input as optional",
			task: task.Task{Name: "t", Worker: "w", Tool: "lookup", Inputs: []task.Input{
				{Name: "doi", Provenance: privacy.Published},
			}},
			want: `requires "doi", which the task declares optional`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := task.Verify(c.task, tool("lookup", lookupSchema))
			if err == nil {
				t.Fatal("drift went unreported")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// A misspelled input name is the case worth naming: the call still
// works, because the argument is simply never supplied, so nothing
// fails at run time — but the provenance that input was declared to
// carry is never contributed either.
func TestVerifyCatchesTheSilentMisspelling(t *testing.T) {
	misspelled := task.Task{
		Name: "demo.lookup", Worker: "w", Tool: "lookup",
		Baseline: privacy.Published,
		Inputs: []task.Input{
			{Name: "doi", Required: true, Provenance: privacy.Published},
			{Name: "notebook_entery", Provenance: privacy.ELabFTW}, // typo
		},
	}

	// The typo does not break a call: the argument is never supplied,
	// so the derived provenance stays Published.
	if got, _ := misspelled.Effective(map[string]any{"doi": "x"}); got != privacy.Published {
		t.Fatalf("Effective = %q, want published — the premise of this test", got)
	}
	// Verify is the only thing that notices.
	err := task.Verify(misspelled, tool("lookup", lookupSchema))
	if err == nil || !strings.Contains(err.Error(), "notebook_entery") {
		t.Fatalf("Verify did not catch the misspelled input: %v", err)
	}
}

func TestVerifyRefusesToPassOnAMissingSchema(t *testing.T) {
	for _, schema := range []string{"", "null"} {
		err := task.Verify(lookupTask(), tool("lookup", schema))
		if !errors.Is(err, task.ErrNoSchema) {
			t.Errorf("schema %q: want ErrNoSchema, got %v", schema, err)
		}
	}
}

func TestVerifyReportsAnUnparseableSchema(t *testing.T) {
	err := task.Verify(lookupTask(), tool("lookup", `{"type": `))
	if err == nil || errors.Is(err, task.ErrNoSchema) {
		t.Fatalf("want a parse error distinct from ErrNoSchema, got %v", err)
	}
}

func TestVerifyAgainstSeparatesVerifiedFromUnverifiable(t *testing.T) {
	tasks := []task.Task{
		lookupTask(), // worker w, tool lookup, agrees
		{Name: "demo.stats", Worker: "w", Tool: "stats", Baseline: privacy.Published},
		{Name: "demo.gone", Worker: "w", Tool: "vanished", Baseline: privacy.Published},
		{Name: "other.thing", Worker: "elsewhere", Tool: "lookup", Baseline: privacy.Published},
	}
	tools := []mcp.Tool{
		tool("lookup", lookupSchema),
		tool("stats", ""), // server publishes no schema
	}

	rep := task.VerifyAgainst(tasks, "w", tools)

	if len(rep.Verified) != 1 || rep.Verified[0] != "demo.lookup" {
		t.Errorf("Verified = %v, want [demo.lookup]", rep.Verified)
	}
	if len(rep.Unverifiable) != 1 || !strings.Contains(rep.Unverifiable[0], "demo.stats") {
		t.Errorf("Unverifiable = %v, want one entry for demo.stats", rep.Unverifiable)
	}
	if len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "vanished") {
		t.Errorf("Problems = %v, want one entry for the missing tool", rep.Problems)
	}
	if rep.OK() {
		t.Error("OK() true with a problem recorded")
	}
	// A task for another worker must not appear anywhere.
	for _, line := range append(append(rep.Verified, rep.Unverifiable...), rep.Problems...) {
		if strings.Contains(line, "other.thing") {
			t.Errorf("a task for another worker appeared in the report: %q", line)
		}
	}
}

// A report that checked nothing must not be indistinguishable from a
// report that found nothing wrong. OK() is true in both cases by
// design, so Summary has to carry the counts.
func TestEmptyReportIsVisiblyEmpty(t *testing.T) {
	rep := task.VerifyAgainst(nil, "w", nil)
	if !rep.OK() {
		t.Error("an empty report should have no problems")
	}
	if got := rep.Summary(); got != "0 verified, 0 unverifiable, 0 problems" {
		t.Errorf("Summary() = %q", got)
	}
}
