// Package bus is the tool boundary Rule 4 talks about.
//
// The privacy package decides whether a payload may go somewhere. This
// package is the reason that decision cannot be skipped: a Transport is
// held privately by a Bus and is reachable only through Dispatch, which
// calls privacy.Check first and returns before touching the transport
// if the answer is no.
//
// That is the whole design. Everything else here exists to remove ways
// around it:
//
//   - A worker's Destination is what routing turns on, and the zero
//     value of privacy.Destination is External, so a Worker literal
//     that omits the field costs a refused request rather than a leak.
//   - An unregistered worker is refused rather than assumed local,
//     because an unknown destination is not a safe one.
//   - Register refuses to replace an existing worker, so a later
//     registration cannot quietly downgrade an external worker to
//     local.
//   - Every dispatch is audited, allowed or refused, and the Event
//     carries no payload — only a length and an offset.
//
// What this package does NOT do: it is not an MCP client, and it does
// not know what a tool is. It routes a payload to a named worker. The
// MCP client and the task-shaped tool wrappers (CGTRU-18) sit above it
// and gain nothing by bypassing it.
package bus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/forfex-lab/forfex/internal/privacy"
)

var (
	// ErrRefused means policy said no. The payload was not sent.
	ErrRefused = errors.New("refused by Rule 4")
	// ErrUnknownWorker means the name was never registered, so its
	// destination is unknown and therefore not safe.
	ErrUnknownWorker = errors.New("unknown worker")
	// ErrNoTransport means the Bus was built without one.
	ErrNoTransport = errors.New("bus has no transport")
)

// Worker is somewhere the orchestrator can send work.
//
// Destination is the only field routing depends on. Leaving it unset
// yields privacy.External, which is the safe direction.
type Worker struct {
	Name        string
	Destination privacy.Destination
}

// Transport delivers an approved payload. Implementations are reached
// only through Bus.Dispatch; nothing in this package hands one out.
type Transport interface {
	Send(ctx context.Context, w Worker, body string) (string, error)
}

// Event is one audit record.
//
// It deliberately has no body field. RunLength and Offset describe
// where a nucleotide run was found without reproducing it, so the log
// stays useful without becoming the leak.
type Event struct {
	Time        time.Time
	Worker      string
	Destination privacy.Destination
	Allowed     bool
	Reason      string
	RunLength   int
	Offset      int
}

// AuditSink receives one Event per dispatch attempt, including attempts
// that were refused and attempts to unknown workers.
type AuditSink interface {
	Record(Event)
}

// Bus routes payloads to workers, checking Rule 4 on the way.
type Bus struct {
	mu        sync.RWMutex
	workers   map[string]Worker
	transport Transport
	audit     AuditSink
	now       func() time.Time
}

// New returns a Bus. A nil audit sink is tolerated and discards events;
// a nil transport is not, and surfaces at Dispatch rather than as a
// panic with a payload in flight.
func New(t Transport, a AuditSink) *Bus {
	return &Bus{
		workers:   make(map[string]Worker),
		transport: t,
		audit:     a,
		now:       time.Now,
	}
}

// Register adds a worker. It refuses an empty name and refuses to
// replace an existing one.
func (b *Bus) Register(w Worker) error {
	if w.Name == "" {
		return errors.New("worker name must not be empty")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.workers[w.Name]; exists {
		return fmt.Errorf("worker %q is already registered", w.Name)
	}
	b.workers[w.Name] = w
	return nil
}

// Dispatch sends p to the named worker, or returns why it did not.
//
// The order matters and is the point of the package: resolve, check,
// audit, and only then send.
func (b *Bus) Dispatch(ctx context.Context, name string, p privacy.Payload) (string, error) {
	b.mu.RLock()
	w, known := b.workers[name]
	b.mu.RUnlock()

	if !known {
		// Audited with the restrictive destination, because that is
		// what an unknown worker is treated as.
		b.record(Event{
			Worker:      name,
			Destination: privacy.External,
			Allowed:     false,
			Reason:      "worker is not registered",
		})
		return "", fmt.Errorf("%w: %q", ErrUnknownWorker, name)
	}

	d := privacy.Check(p, w.Destination)

	b.record(Event{
		Worker:      w.Name,
		Destination: w.Destination,
		Allowed:     d.Allowed,
		Reason:      d.Reason,
		RunLength:   d.RunLength,
		Offset:      d.Offset,
	})

	if !d.Allowed {
		return "", fmt.Errorf("%w: %s", ErrRefused, d.Reason)
	}

	if b.transport == nil {
		return "", ErrNoTransport
	}
	return b.transport.Send(ctx, w, p.Body)
}

func (b *Bus) record(e Event) {
	if b.audit == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = b.now()
	}
	b.audit.Record(e)
}
