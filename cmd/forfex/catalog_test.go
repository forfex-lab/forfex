package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/forfex-lab/forfex/internal/privacy"
)

// The catalogue is built with MustRegister, which panics on a malformed
// definition — so a broken entry would take the binary down on the
// first `forfex task` rather than at build time. This is the test that
// makes it a build-time failure instead.
func TestCatalogIsValidAndNotEmpty(t *testing.T) {
	c := catalog()
	tasks := c.All()
	if len(tasks) == 0 {
		t.Fatal("the catalogue is empty; every assertion below would pass vacuously")
	}
	for _, tk := range tasks {
		if err := tk.Validate(); err != nil {
			t.Errorf("%s: %v", tk.Name, err)
		}
		if tk.Summary == "" {
			t.Errorf("%s has no summary; `forfex task list` is the only documentation a caller gets", tk.Name)
		}
		for _, in := range tk.Inputs {
			if in.Summary == "" {
				t.Errorf("%s input %q has no summary", tk.Name, in.Name)
			}
		}
	}
}

// catalog.go ends with a paragraph explaining that every write tool is
// deliberately absent — iwe_normalize above all, which rewrites every
// document in the library. A comment is not a constraint. This is.
func TestCatalogCallsNoToolThatWrites(t *testing.T) {
	writes := map[string]string{
		"iwe_create":    "creates a document",
		"iwe_update":    "overwrites a document",
		"iwe_delete":    "deletes a document",
		"iwe_rename":    "moves a document and rewrites links to it",
		"iwe_extract":   "splits a document",
		"iwe_inline":    "merges documents",
		"iwe_squash":    "collapses a subtree",
		"iwe_attach":    "edits the target documents",
		"iwe_normalize": "rewrites EVERY document in the library",
	}
	for _, tk := range catalog().All() {
		if why, isWrite := writes[tk.Tool]; isWrite {
			t.Errorf("task %s calls %s, which %s. Adding a write task is a deliberate "+
				"decision about when an orchestrator may edit the graph unattended; "+
				"make it in a commit that says so, and update this test with it.",
				tk.Name, tk.Tool, why)
		}
	}
}

// An input declared `published` is one a caller may send to an external
// model, so each one is a judgement that should be visible in review
// rather than accumulating by default. The list here is the reviewed
// set; a new one has to be added deliberately.
func TestPublishedInputsAreTheReviewedOnes(t *testing.T) {
	reviewed := map[string]bool{
		"vault.find/limit":          true,
		"vault.retrieve/limit":      true,
		"vault.retrieve/max_tokens": true,
	}
	for _, tk := range catalog().All() {
		for _, in := range tk.Inputs {
			if in.Provenance != privacy.Published {
				continue
			}
			key := tk.Name + "/" + in.Name
			if !reviewed[key] {
				t.Errorf("%s is declared published and is not in the reviewed set. "+
					"An argument carrying caller text must not be declared published; "+
					"if this one genuinely carries none, add it here in the same commit.", key)
			}
		}
	}
}

// The catalogue's baselines are all `published`, which is only safe
// because no task has a required input that carries caller text: a
// baseline applies to the call with nothing supplied. If a task ever
// requires such an input, the baseline stops being the provenance of a
// realisable call.
func TestPublishedBaselineImpliesNoRequiredRestrictedInput(t *testing.T) {
	for _, tk := range catalog().All() {
		if tk.Baseline != privacy.Published {
			continue
		}
		for _, in := range tk.Inputs {
			if in.Required && in.Provenance != privacy.Published {
				t.Errorf("%s has a published baseline but requires %q, which is %s: "+
					"the baseline then describes a call that cannot be made",
					tk.Name, in.Name, in.Provenance.Describe())
			}
		}
	}
}

// TestTaskExplainExitCodes pins the behaviour a human checks by hand.
// Two of these cases are the same task differing only in whether one
// argument was supplied, which is the mechanism in one pair of rows.
func TestTaskExplainExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
		grep string
	}{
		{
			name: "no arguments: the baseline decides",
			args: []string{"-task", "vault.stats", "-destination", "external"},
			want: 0,
			grep: "published (baseline)",
		},
		{
			name: "one document key raises it",
			args: []string{"-task", "vault.stats", "-destination", "external",
				"-args", `{"key":"evidence/R03-knipping-2022-hspc"}`},
			want: 1,
			grep: `from input "key"`,
		},
		{
			name: "a local worker takes it either way",
			args: []string{"-task", "vault.stats", "-destination", "local",
				"-args", `{"key":"evidence/R03-knipping-2022-hspc"}`},
			want: 0,
		},
		{
			name: "an integer cap stays routable",
			args: []string{"-task", "vault.find", "-destination", "external",
				"-args", `{"limit":10}`},
			want: 0,
		},
		{
			name: "a search string does not",
			args: []string{"-task", "vault.find", "-destination", "external",
				"-args", `{"lexical":"CCR5"}`},
			want: 1,
		},
		// Usage errors stay distinguishable from refusals, as they do
		// for `forfex route`.
		{
			name: "an undeclared argument is a usage error, not a refusal",
			args: []string{"-task", "vault.find", "-args", `{"sneak":"x"}`},
			want: 2,
			grep: `does not accept "sneak"`,
		},
		{
			name: "unknown task",
			args: []string{"-task", "vault.nope"},
			want: 2,
		},
		{
			name: "missing -task",
			args: []string{"-destination", "local"},
			want: 2,
		},
		{
			name: "args that are not a JSON object",
			args: []string{"-task", "vault.stats", "-args", "["},
			want: 2,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			got := taskExplainCmd(c.args, &out, &errb)
			if got != c.want {
				t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)",
					got, c.want, out.String(), errb.String())
			}
			if c.grep != "" && !strings.Contains(out.String()+errb.String(), c.grep) {
				t.Errorf("output does not contain %q:\nstdout %s\nstderr %s",
					c.grep, out.String(), errb.String())
			}
		})
	}
}

// `forfex task explain` builds its bus with a nil transport so it is
// structurally unable to send. If that ever changes, this is the test
// that says so: an allowed verdict must come back as ErrNoTransport,
// which the command reports with "nothing was sent".
func TestTaskExplainSendsNothingWhenAllowed(t *testing.T) {
	var out, errb bytes.Buffer
	if got := taskExplainCmd([]string{"-task", "vault.stats", "-destination", "external"},
		&out, &errb); got != 0 {
		t.Fatalf("exit = %d (stderr %q)", got, errb.String())
	}
	if !strings.Contains(out.String(), "nothing was sent") {
		t.Errorf("an allowed explain did not report that nothing was sent: %q", out.String())
	}
}

func TestTaskSubcommandRouting(t *testing.T) {
	var out, errb bytes.Buffer
	if got := taskCmd(nil, &out, &errb); got != 2 {
		t.Errorf("no subcommand: exit = %d, want 2", got)
	}
	if got := taskCmd([]string{"wat"}, &out, &errb); got != 2 {
		t.Errorf("unknown subcommand: exit = %d, want 2", got)
	}
	out.Reset()
	if got := taskCmd([]string{"list"}, &out, &errb); got != 0 {
		t.Errorf("list: exit = %d, want 0", got)
	}
	// list is the only documentation of a declaration, so it has to
	// print the provenance rather than just the names.
	for _, want := range []string{"vault.stats", "iwe_stats", "published", "unknown (treated as unpublished)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("`task list` output is missing %q", want)
		}
	}
}
