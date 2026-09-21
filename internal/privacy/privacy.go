// Package privacy enforces Rule 4 — sequence privacy — at the tool
// boundary, in Go, rather than by prompt.
//
// Two independent reasons to refuse a payload an external destination:
//
//  1. Provenance. Unpublished lab sequence and anything out of eLabFTW is
//     local-inference-only, whatever it contains.
//  2. Content. A run of 20 or more consecutive nucleotide characters,
//     however the payload is labelled.
//
// Both fail closed. An unlabelled payload is treated as unpublished, and
// the zero value of Provenance is the unsafe-to-send one, so a struct
// literal that omits the field cannot accidentally earn external routing.
//
// What this package does NOT do, stated so it is not mistaken for
// coverage it lacks:
//
//   - IUPAC ambiguity codes (N, R, Y, W, S…) are not counted as
//     nucleotides, so they break a run. A sequence written with
//     ambiguity codes every few bases can pass. Widening the alphabet
//     would also widen false positives over ordinary prose; the gap is
//     recorded rather than papered over.
//   - Protein sequence is not detected at all.
//   - Encoded or compressed payloads are not decoded before scanning.
//
// It is a boundary check against accidental disclosure, not an
// exfiltration-resistant control against a determined caller.
package privacy

// Provenance records where a payload came from. The zero value is
// Unknown, and Unknown is treated as Unpublished.
type Provenance string

const (
	// Unknown is the zero value: provenance was never established.
	Unknown Provenance = ""
	// Published material is in the public literature already.
	Published Provenance = "published"
	// Unpublished is lab-generated and not yet disclosed.
	Unpublished Provenance = "unpublished"
	// ELabFTW is anything read out of the lab notebook.
	ELabFTW Provenance = "elabftw"
)

// Destination is where a payload is about to be sent.
type Destination int

const (
	// Local inference runs on this machine. Rule 4 routes sequence here.
	Local Destination = iota
	// External is any model or service off this machine — Claude, Codex,
	// Grok, opencode, or a hosted MCP endpoint.
	External
)

func (d Destination) String() string {
	if d == Local {
		return "local"
	}
	return "external"
}

// Payload is a unit of content about to cross the boundary.
type Payload struct {
	// Provenance should always be set explicitly. Leaving it zero is
	// safe but pessimistic: the payload is treated as unpublished.
	Provenance Provenance
	Body       string
}

// Decision is the result of a check. Reason is written to the audit log
// and therefore never quotes the payload — a filter that echoed the
// sequence it just blocked would leak it into the log.
type Decision struct {
	Allowed bool
	Reason  string
	// RunLength is the longest nucleotide run found, 0 if none. It is a
	// length, not the content.
	RunLength int
	// Offset is where that run starts, in bytes of the normalized text.
	Offset int
}

// MinRun is the threshold from Rule 4: 20 or more consecutive
// nucleotides may not reach an external model.
const MinRun = 20

// Check decides whether payload may be sent to dest.
func Check(p Payload, dest Destination) Decision {
	run, off := longestNucleotideRun(p.Body)

	// Local is where sequence is supposed to go. Neither rule applies.
	if dest == Local {
		return Decision{Allowed: true, RunLength: run, Offset: off}
	}

	if !p.Provenance.externalSafe() {
		return Decision{
			Allowed:   false,
			Reason:    "provenance " + p.Provenance.describe() + " is local-inference-only (Rule 4)",
			RunLength: run,
			Offset:    off,
		}
	}

	if run >= MinRun {
		return Decision{
			Allowed:   false,
			Reason:    "payload contains a nucleotide run of " + itoa(run) + " characters, at or over the limit of " + itoa(MinRun) + " (Rule 4)",
			RunLength: run,
			Offset:    off,
		}
	}

	return Decision{Allowed: true, RunLength: run, Offset: off}
}

func (p Provenance) externalSafe() bool {
	return p == Published
}

func (p Provenance) describe() string {
	if p == Unknown {
		return "unknown (treated as unpublished)"
	}
	return string(p)
}

// longestNucleotideRun returns the length of the longest run of
// nucleotide characters and where it starts.
//
// Whitespace and digits are skipped rather than breaking a run. This is
// the difference between catching line-wrapped FASTA and missing it: at
// 60 characters per line, a contiguous-only scan sees 60-character runs
// of a 3,000-base sequence, which is still over the threshold, but at 10
// characters per line it sees nothing. GenBank flatfiles interleave line
// numbers and spaces for the same reason. Skipping both closes the gap
// at the cost of treating "A C G T …" in prose as a run, which is a
// false positive in the safe direction.
func longestNucleotideRun(s string) (length, offset int) {
	best, bestOff := 0, 0
	cur, curOff := 0, 0

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case isNucleotide(c):
			if cur == 0 {
				curOff = i
			}
			cur++
			if cur > best {
				best, bestOff = cur, curOff
			}
		case isSkippable(c):
			// Neither extends nor breaks the run.
		default:
			cur = 0
		}
	}
	return best, bestOff
}

func isNucleotide(c byte) bool {
	switch c {
	case 'A', 'C', 'G', 'T', 'U', 'a', 'c', 'g', 't', 'u':
		return true
	}
	return false
}

// isSkippable reports characters that sequence formats interleave with
// the bases themselves: line wrapping, block spacing, and the position
// numbers in a GenBank flatfile.
func isSkippable(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return c >= '0' && c <= '9'
}

// itoa avoids pulling strconv in for one call, and keeps the reason
// string free of anything derived from the payload's content.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
