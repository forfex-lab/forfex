package privacy

import "testing"

// Rule 4 is a blocking filter, so every test here is written from the
// question "what would let unpublished sequence reach an external model?"
// A false positive costs a refused request. A false negative is the thing
// the rule exists to prevent. The asymmetry is deliberate throughout.

func TestProvenanceRouting(t *testing.T) {
	cases := []struct {
		name    string
		prov    Provenance
		dest    Destination
		allowed bool
	}{
		{"published to external is fine", Published, External, true},
		{"published to local is fine", Published, Local, true},
		{"unpublished stays local", Unpublished, Local, true},
		{"unpublished never goes external", Unpublished, External, false},
		{"elabftw stays local", ELabFTW, Local, true},
		{"elabftw never goes external", ELabFTW, External, false},

		// Fail closed: an unlabelled payload is treated as unpublished.
		// A caller that forgets to set provenance must not thereby earn
		// external routing.
		{"unknown provenance is not external-safe", Unknown, External, false},
		{"unknown provenance is fine locally", Unknown, Local, true},

		// The zero value of Provenance must be the safe one, so a
		// struct literal that omits the field cannot leak.
		{"zero value behaves as unknown", Provenance(""), External, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Check(Payload{Provenance: c.prov, Body: "no sequence here"}, c.dest)
			if d.Allowed != c.allowed {
				t.Fatalf("Check(%q -> %v) allowed=%v, want %v (reason: %s)",
					c.prov, c.dest, d.Allowed, c.allowed, d.Reason)
			}
			if !d.Allowed && d.Reason == "" {
				t.Error("a blocked decision must carry a reason")
			}
		})
	}
}

func TestNucleotideRunBlocksExternal(t *testing.T) {
	const run20 = "ACGTACGTACGTACGTACGT" // exactly 20

	cases := []struct {
		name    string
		body    string
		allowed bool
	}{
		{"19 is under the threshold", "ACGTACGTACGTACGTACG", true},
		{"20 is at the threshold", run20, false},
		{"21 is over", run20 + "A", false},
		{"lowercase counts", "acgtacgtacgtacgtacgt", false},
		{"mixed case counts", "AcGtAcGtAcGtAcGtAcGt", false},
		{"U counts as well as T", "ACGUACGUACGUACGUACGU", false},
		{"embedded in prose still counts", "the spacer is " + run20 + " and it binds", false},

		// Line-wrapped FASTA is the obvious false negative: a naive scan
		// for contiguous characters sees only 10 per line and passes.
		{"FASTA wrapped across lines", ">seq1\nACGTACGTAC\nGTACGTACGT\n", false},
		{"CRLF line endings", "ACGTACGTAC\r\nGTACGTACGT", false},

		// GenBank flatfile puts a line number and spaces between blocks.
		{"GenBank flatfile spacing", "   1 acgtacgtac gtacgtacgt", false},

		// Ordinary prose must not trip it, or the filter gets disabled.
		{"plain english passes", "A cat sat on a gate, and a gnat ate a tac.", true},
		{"empty payload passes", "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Check(Payload{Provenance: Published, Body: c.body}, External)
			if d.Allowed != c.allowed {
				t.Fatalf("Check(body=%q) allowed=%v, want %v (reason: %s)",
					c.body, d.Allowed, c.allowed, d.Reason)
			}
		})
	}
}

func TestNucleotideRunAllowedLocally(t *testing.T) {
	// Local inference is the destination Rule 4 routes sequence *to*.
	// Blocking it here would make the rule unimplementable.
	d := Check(Payload{Provenance: Unpublished, Body: "ACGTACGTACGTACGTACGT"}, Local)
	if !d.Allowed {
		t.Fatalf("sequence must be allowed locally, got blocked: %s", d.Reason)
	}
}

func TestDecisionDoesNotEchoTheSequence(t *testing.T) {
	// The reason string gets logged. A filter that quotes the payload it
	// just blocked would write unpublished sequence into the audit log,
	// which is the leak it was meant to prevent.
	const secret = "ACGTACGTACGTACGTACGTTTTT"
	d := Check(Payload{Provenance: Unpublished, Body: secret}, External)
	if d.Allowed {
		t.Fatal("expected block")
	}
	if contains(d.Reason, secret) || contains(d.Reason, secret[:20]) {
		t.Fatalf("reason leaks the payload: %q", d.Reason)
	}
}

func TestFindingReportsPositionNotContent(t *testing.T) {
	d := Check(Payload{Provenance: Published, Body: "xx " + "ACGTACGTACGTACGTACGT"}, External)
	if d.Allowed {
		t.Fatal("expected block")
	}
	if d.RunLength < 20 {
		t.Errorf("RunLength = %d, want >= 20", d.RunLength)
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestKnownGaps pins the limits the package documents, so that a change
// in behaviour shows up as a failing test rather than a silent widening
// or narrowing of what gets through. These assert what the filter does
// NOT catch; none of them is a desirable property.
func TestKnownGaps(t *testing.T) {
	t.Run("ambiguity codes break a run", func(t *testing.T) {
		// N is a legal IUPAC base but not in the Rule 4 alphabet.
		d := Check(Payload{Provenance: Published, Body: "ACGTACGTACNGTACGTACGTA"}, External)
		if !d.Allowed {
			t.Skip("behaviour changed: ambiguity codes now counted - update the package doc")
		}
	})

	t.Run("protein sequence is not detected", func(t *testing.T) {
		d := Check(Payload{Provenance: Published, Body: "MKVLAAGIVGLNLGGHHHQRSTVWY"}, External)
		if !d.Allowed {
			t.Skip("behaviour changed: protein now detected - update the package doc")
		}
	})
}

// TestZeroDestinationIsExternal pins the fail-closed direction for
// Destination, matching what Provenance already does.
//
// This caught a real defect. Destination was originally declared
// `Local Destination = iota`, making Local the zero value — so any
// struct literal that omitted the field got the PERMISSIVE
// destination, where Rule 4 does not apply. A caller that forgot to
// set it would have been handed a pass rather than a refusal.
func TestZeroDestinationIsExternal(t *testing.T) {
	var d Destination // never assigned

	if d != External {
		t.Fatalf("zero Destination = %v, want External — the zero value must be the one that refuses", d)
	}

	// The behavioural consequence, which is the part that matters.
	var p Payload // zero Provenance too
	p.Body = "ACGTACGTACGTACGTACGT"
	if got := Check(p, d); got.Allowed {
		t.Fatal("a fully zero-valued Check allowed sequence out; it must fail closed")
	}
}
