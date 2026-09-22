package bus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/forfex-lab/forfex/internal/privacy"
)

// The privacy package decides. This package is the thing that cannot be
// bypassed. So every test here asks one question: is there a path by
// which a payload reaches a transport without a decision first?
//
// spyTransport counts sends and remembers what it was handed, so a test
// can assert on "was the transport touched at all" rather than only on
// the returned error. A refusal that still called out would return an
// error AND leak, and the error alone would look like success.

type spyTransport struct {
	calls  int
	bodies []string
	err    error
}

func (s *spyTransport) Send(_ context.Context, _ Worker, body string) (string, error) {
	s.calls++
	s.bodies = append(s.bodies, body)
	if s.err != nil {
		return "", s.err
	}
	return "ok", nil
}

type spyAudit struct{ events []Event }

func (s *spyAudit) Record(e Event) { s.events = append(s.events, e) }

func newTestBus(t *testing.T, workers ...Worker) (*Bus, *spyTransport, *spyAudit) {
	t.Helper()
	tp, au := &spyTransport{}, &spyAudit{}
	b := New(tp, au)
	for _, w := range workers {
		if err := b.Register(w); err != nil {
			t.Fatalf("Register(%q): %v", w.Name, err)
		}
	}
	return b, tp, au
}

const run20 = "ACGTACGTACGTACGTACGT"

// TestRestrictedPayloadNeverReachesExternalWorker is CGTRU-18's
// acceptance test, stated in the issue as "tests that prove a
// restricted payload cannot reach an external worker".
func TestRestrictedPayloadNeverReachesExternalWorker(t *testing.T) {
	cases := []struct {
		name string
		p    privacy.Payload
	}{
		{"unpublished provenance", privacy.Payload{Provenance: privacy.Unpublished, Body: "a design note"}},
		{"elabftw provenance", privacy.Payload{Provenance: privacy.ELabFTW, Body: "a notebook entry"}},
		{"unset provenance", privacy.Payload{Body: "a payload nobody labelled"}},
		{"published but 20 nucleotides", privacy.Payload{Provenance: privacy.Published, Body: run20}},
		{"sequence buried in prose", privacy.Payload{Provenance: privacy.Published, Body: "the spacer is " + run20 + " ok"}},
		{"line-wrapped FASTA", privacy.Payload{Provenance: privacy.Published, Body: ">s\nACGTACGTAC\nGTACGTACGT\n"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, tp, _ := newTestBus(t, Worker{Name: "claude", Destination: privacy.External})

			_, err := b.Dispatch(context.Background(), "claude", c.p)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("Dispatch err = %v, want ErrRefused", err)
			}
			// The load-bearing assertion. An error return is not enough;
			// the transport must never have been reached.
			if tp.calls != 0 {
				t.Fatalf("transport called %d times on a refused dispatch; it must be 0", tp.calls)
			}
		})
	}
}

func TestLocalWorkerReceivesRestrictedPayload(t *testing.T) {
	// Local inference is where Rule 4 routes sequence TO. Refusing here
	// would make the rule unimplementable rather than strict.
	b, tp, _ := newTestBus(t, Worker{Name: "ollama", Destination: privacy.Local})

	got, err := b.Dispatch(context.Background(), "ollama",
		privacy.Payload{Provenance: privacy.ELabFTW, Body: run20})
	if err != nil {
		t.Fatalf("Dispatch to local worker: %v", err)
	}
	if got != "ok" || tp.calls != 1 {
		t.Fatalf("got %q after %d calls, want \"ok\" after 1", got, tp.calls)
	}
}

func TestPublishedNonSequenceReachesExternalWorker(t *testing.T) {
	// The positive control. A bus that refused everything would pass
	// every test above while being useless.
	b, tp, _ := newTestBus(t, Worker{Name: "claude", Destination: privacy.External})

	got, err := b.Dispatch(context.Background(), "claude",
		privacy.Payload{Provenance: privacy.Published, Body: "summarise doi:10.1038/s41467-025-55873-3"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != "ok" || tp.calls != 1 {
		t.Fatalf("got %q after %d calls, want \"ok\" after 1", got, tp.calls)
	}
}

func TestUnknownWorkerIsRefused(t *testing.T) {
	// Fail closed: an unregistered name has no known destination, so it
	// cannot be assumed local.
	b, tp, _ := newTestBus(t)

	_, err := b.Dispatch(context.Background(), "mystery",
		privacy.Payload{Provenance: privacy.Published, Body: "harmless"})
	if !errors.Is(err, ErrUnknownWorker) {
		t.Fatalf("err = %v, want ErrUnknownWorker", err)
	}
	if tp.calls != 0 {
		t.Fatalf("transport called %d times for an unknown worker", tp.calls)
	}
}

func TestWorkerRegisteredWithoutDestinationIsExternal(t *testing.T) {
	// A Worker literal that omits Destination gets the zero value. That
	// value must be External, so the omission costs a refused request
	// rather than a leak.
	b, tp, _ := newTestBus(t, Worker{Name: "forgot"})

	_, err := b.Dispatch(context.Background(), "forgot",
		privacy.Payload{Provenance: privacy.Unpublished, Body: "lab note"})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused — an unset Destination must not be treated as local", err)
	}
	if tp.calls != 0 {
		t.Fatalf("transport called %d times", tp.calls)
	}
}

func TestRegisterRejectsDuplicateAndEmptyNames(t *testing.T) {
	b, _, _ := newTestBus(t)

	if err := b.Register(Worker{Name: ""}); err == nil {
		t.Error("Register accepted an empty name")
	}
	if err := b.Register(Worker{Name: "w", Destination: privacy.Local}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	// Silently replacing a worker would let a later registration
	// downgrade an external worker to local.
	if err := b.Register(Worker{Name: "w", Destination: privacy.Local}); err == nil {
		t.Error("Register silently replaced an existing worker")
	}
}

func TestEveryDispatchIsAudited(t *testing.T) {
	b, _, au := newTestBus(t,
		Worker{Name: "claude", Destination: privacy.External},
		Worker{Name: "ollama", Destination: privacy.Local},
	)
	ctx := context.Background()

	_, _ = b.Dispatch(ctx, "claude", privacy.Payload{Provenance: privacy.Published, Body: "fine"})
	_, _ = b.Dispatch(ctx, "claude", privacy.Payload{Provenance: privacy.Unpublished, Body: "secret"})
	_, _ = b.Dispatch(ctx, "ollama", privacy.Payload{Provenance: privacy.ELabFTW, Body: run20})
	_, _ = b.Dispatch(ctx, "nobody", privacy.Payload{Body: "x"})

	if len(au.events) != 4 {
		t.Fatalf("recorded %d events, want 4 — a refusal that is not audited is invisible", len(au.events))
	}
	if au.events[0].Allowed != true || au.events[1].Allowed != false {
		t.Errorf("allowed flags wrong: %v, %v", au.events[0].Allowed, au.events[1].Allowed)
	}
	if au.events[1].Reason == "" {
		t.Error("a refused event carries no reason")
	}
}

func TestAuditNeverCarriesThePayload(t *testing.T) {
	// The audit log is written to disk. A record that quoted the
	// payload it just refused would write unpublished sequence into the
	// log — the leak the check exists to prevent, one layer down.
	const secret = "ACGTACGTACGTACGTACGTTTTT"
	b, _, au := newTestBus(t, Worker{Name: "claude", Destination: privacy.External})

	_, _ = b.Dispatch(context.Background(), "claude",
		privacy.Payload{Provenance: privacy.Unpublished, Body: secret})

	if len(au.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(au.events))
	}
	e := au.events[0]
	for _, needle := range []string{secret, secret[:20], "ACGTACGTAC"} {
		if strings.Contains(e.Reason, needle) {
			t.Fatalf("audit reason leaks the payload: %q", e.Reason)
		}
	}
	if e.RunLength < 20 {
		t.Errorf("RunLength = %d, want >= 20 — length is safe to record, content is not", e.RunLength)
	}
}

func TestTransportFailurePropagatesAndIsNotARefusal(t *testing.T) {
	// A transport error must not be mistaken for a privacy refusal;
	// they need different handling upstream.
	boom := errors.New("connection reset")
	tp, au := &spyTransport{err: boom}, &spyAudit{}
	b := New(tp, au)
	if err := b.Register(Worker{Name: "claude", Destination: privacy.External}); err != nil {
		t.Fatal(err)
	}

	_, err := b.Dispatch(context.Background(), "claude",
		privacy.Payload{Provenance: privacy.Published, Body: "fine"})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the transport error", err)
	}
	if errors.Is(err, ErrRefused) {
		t.Error("a transport failure was reported as a privacy refusal")
	}
	if !au.events[0].Allowed {
		t.Error("the dispatch was allowed by policy; the audit should say so even though delivery failed")
	}
}

func TestNilTransportIsRefusedNotPanicked(t *testing.T) {
	// Constructing a Bus without a transport is a programming error.
	// It must not become a nil dereference at the moment a payload is
	// in flight.
	b := New(nil, &spyAudit{})
	if err := b.Register(Worker{Name: "w", Destination: privacy.Local}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Dispatch(context.Background(), "w", privacy.Payload{Provenance: privacy.Published}); err == nil {
		t.Error("Dispatch with a nil transport returned no error")
	}
}
