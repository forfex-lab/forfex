package main

import (
	"bytes"
	"strings"
	"testing"
)

// The CLI is the hand-checkable face of Rule 4, so its exit codes are an
// interface: 0 allowed, 1 refused, 2 the caller got it wrong. A script
// that greps output would break silently; one that checks $? should not.
func TestRouteExitCodes(t *testing.T) {
	const run20 = "ACGTACGTACGTACGTACGT"

	cases := []struct {
		name string
		body string
		args []string
		want int
	}{
		{"published prose to external", "summarise this",
			[]string{"-worker", "claude", "-destination", "external", "-provenance", "published"}, 0},
		{"unpublished to external", "a design note",
			[]string{"-worker", "claude", "-destination", "external", "-provenance", "unpublished"}, 1},
		{"elabftw to external", "notebook",
			[]string{"-worker", "claude", "-destination", "external", "-provenance", "elabftw"}, 1},
		{"unlabelled defaults to refused", "who knows",
			[]string{"-worker", "claude", "-destination", "external"}, 1},
		{"twenty nucleotides to external", run20,
			[]string{"-worker", "claude", "-destination", "external", "-provenance", "published"}, 1},
		{"nineteen is under the threshold", "ACGTACGTACGTACGTACG",
			[]string{"-worker", "claude", "-destination", "external", "-provenance", "published"}, 0},
		{"sequence to local is the point of the rule", run20,
			[]string{"-worker", "ollama", "-destination", "local", "-provenance", "elabftw"}, 0},

		// Usage errors must not be confused with a refusal, or a typo
		// in a script reads as "Rule 4 said no".
		{"unknown destination", "x",
			[]string{"-worker", "claude", "-destination", "maybe"}, 2},
		{"unknown provenance", "x",
			[]string{"-worker", "claude", "-destination", "local", "-provenance", "vibes"}, 2},
		{"missing worker", "x",
			[]string{"-destination", "local"}, 2},
		{"missing destination", "x",
			[]string{"-worker", "claude"}, 2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			got := routeCmd(c.args, strings.NewReader(c.body), &out, &errb)
			if got != c.want {
				t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", got, c.want, out.String(), errb.String())
			}
		})
	}
}

func TestRouteNeverEchoesTheBody(t *testing.T) {
	// This command is meant to be safe to run on material that must not
	// be logged, so neither stream may contain the payload.
	const secret = "ACGTACGTACGTACGTACGTTTTT"
	var out, errb bytes.Buffer

	routeCmd([]string{"-worker", "claude", "-destination", "external", "-provenance", "published"},
		strings.NewReader(secret), &out, &errb)

	for name, s := range map[string]string{"stdout": out.String(), "stderr": errb.String()} {
		if strings.Contains(s, "ACGT") {
			t.Errorf("%s echoed the payload: %q", name, s)
		}
	}
	if !strings.Contains(out.String(), "20") {
		t.Errorf("stdout should report the run LENGTH: %q", out.String())
	}
}
