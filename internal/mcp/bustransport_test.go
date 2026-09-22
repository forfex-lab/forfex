package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/forfex-lab/forfex/internal/bus"
	"github.com/forfex-lab/forfex/internal/privacy"
)

// countingRegistry wraps a Registry so a test can assert that the MCP
// client was never reached, not merely that an error came back.
type countingRegistry struct {
	*Registry
	sends int
}

func (c *countingRegistry) Send(ctx context.Context, w bus.Worker, body string) (string, error) {
	c.sends++
	return c.Registry.Send(ctx, w, body)
}

func newWiredBus(t *testing.T, dest privacy.Destination, h func(string, json.RawMessage) (any, *rpcError)) (*bus.Bus, *countingRegistry) {
	t.Helper()
	c, _ := newFakeServer(t, h)
	mustInit(t, c)

	reg := NewRegistry()
	if err := reg.Add("tool-server", c); err != nil {
		t.Fatal(err)
	}
	cr := &countingRegistry{Registry: reg}

	b := bus.New(cr, nil)
	if err := b.Register(bus.Worker{Name: "tool-server", Destination: dest}); err != nil {
		t.Fatal(err)
	}
	return b, cr
}

// TestSequenceInAnyArgumentIsCaught is the reason the whole call is
// serialised before the check rather than one field being inspected.
//
// A design that scanned an argument named "sequence" would pass every
// one of these except the first.
func TestSequenceInAnyArgumentIsCaught(t *testing.T) {
	const run20 = "ACGTACGTACGTACGTACGT"

	cases := []struct {
		name string
		call ToolCall
	}{
		{"the obvious field name", ToolCall{Tool: "blast", Arguments: map[string]any{"sequence": run20}}},
		{"a field nobody predicted", ToolCall{Tool: "blast", Arguments: map[string]any{"q": run20}}},
		{"buried in prose", ToolCall{Tool: "note", Arguments: map[string]any{"text": "the spacer is " + run20 + " ok"}}},
		{"inside a nested object", ToolCall{Tool: "run", Arguments: map[string]any{
			"opts": map[string]any{"target": run20}}}},
		{"inside an array", ToolCall{Tool: "run", Arguments: map[string]any{
			"batch": []any{"harmless", run20}}}},
		{"in the tool name itself", ToolCall{Tool: run20, Arguments: map[string]any{"x": 1}}},
		{"line-wrapped across a JSON string", ToolCall{Tool: "load", Arguments: map[string]any{
			"fasta": ">s\nACGTACGTAC\nGTACGTACGT\n"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, reg := newWiredBus(t, privacy.External, defaultHandler)

			_, err := Dispatch(context.Background(), b, "tool-server", c.call, privacy.Published)
			if !errors.Is(err, bus.ErrRefused) {
				t.Fatalf("err = %v, want ErrRefused", err)
			}
			if reg.sends != 0 {
				t.Fatalf("the transport was reached %d times on a refused call", reg.sends)
			}
		})
	}
}

func TestRestrictedProvenanceNeverReachesAnExternalToolServer(t *testing.T) {
	for _, prov := range []privacy.Provenance{privacy.Unpublished, privacy.ELabFTW, privacy.Unknown} {
		t.Run(prov.Describe(), func(t *testing.T) {
			b, reg := newWiredBus(t, privacy.External, defaultHandler)

			_, err := Dispatch(context.Background(), b, "tool-server",
				ToolCall{Tool: "echo", Arguments: map[string]any{"text": "nothing sensitive"}}, prov)
			if !errors.Is(err, bus.ErrRefused) {
				t.Fatalf("err = %v, want ErrRefused", err)
			}
			if reg.sends != 0 {
				t.Fatalf("transport reached %d times", reg.sends)
			}
		})
	}
}

// TestSequenceReachesALocalToolServer is the positive control, and it
// is the whole point of Rule 4 rather than an exception to it: a
// containerised aligner on this machine is exactly where sequence is
// supposed to go.
func TestSequenceReachesALocalToolServer(t *testing.T) {
	b, reg := newWiredBus(t, privacy.Local, func(m string, p json.RawMessage) (any, *rpcError) {
		if m == "tools/call" {
			return ToolResult{Content: []Content{{Type: "text", Text: "aligned"}}}, nil
		}
		return defaultHandler(m, p)
	})

	res, err := Dispatch(context.Background(), b, "tool-server",
		ToolCall{Tool: "align", Arguments: map[string]any{"sequence": "ACGTACGTACGTACGTACGT"}},
		privacy.ELabFTW)
	if err != nil {
		t.Fatalf("Dispatch to a local tool server: %v", err)
	}
	if res.Text() != "aligned" || reg.sends != 1 {
		t.Fatalf("res=%q sends=%d, want \"aligned\" and 1", res.Text(), reg.sends)
	}
}

func TestToolLevelErrorSurvivesTheRoundTrip(t *testing.T) {
	// IsError must not be flattened into a Go error by the bus hop, or
	// a deterministic tool failure becomes indistinguishable from a
	// transport problem on the far side.
	b, _ := newWiredBus(t, privacy.Local, func(m string, p json.RawMessage) (any, *rpcError) {
		if m == "tools/call" {
			return ToolResult{Content: []Content{{Type: "text", Text: "bad input"}}, IsError: true}, nil
		}
		return defaultHandler(m, p)
	})

	res, err := Dispatch(context.Background(), b, "tool-server",
		ToolCall{Tool: "x"}, privacy.Published)
	if err != nil {
		t.Fatalf("a tool-level error must not become a Go error: %v", err)
	}
	if !res.IsError || res.Text() != "bad input" {
		t.Fatalf("res = %+v", res)
	}
}

func TestRegistryRefusesDuplicateAndNilBindings(t *testing.T) {
	c, _ := newFakeServer(t, defaultHandler)
	r := NewRegistry()

	if err := r.Add("", c); err == nil {
		t.Error("Add accepted an empty worker name")
	}
	if err := r.Add("w", nil); err == nil {
		t.Error("Add accepted a nil client")
	}
	if err := r.Add("w", c); err != nil {
		t.Fatal(err)
	}
	// Replacing silently could redirect a worker at a different server.
	if err := r.Add("w", c); err == nil {
		t.Error("Add silently replaced an existing binding")
	}
}

func TestUnboundWorkerIsAnErrorNotAPanic(t *testing.T) {
	reg := NewRegistry()
	b := bus.New(reg, nil)
	if err := b.Register(bus.Worker{Name: "ghost", Destination: privacy.Local}); err != nil {
		t.Fatal(err)
	}

	_, err := Dispatch(context.Background(), b, "ghost", ToolCall{Tool: "x"}, privacy.Published)
	if err == nil || !strings.Contains(err.Error(), "no client registered") {
		t.Fatalf("err = %v, want a clear no-client error", err)
	}
}

func TestDispatchIsTheOnlyExportedPathFromAToolCall(t *testing.T) {
	// Registry.Send takes a serialised body and is reachable only as a
	// bus.Transport. If it ever grows an exported sibling that accepts a
	// ToolCall directly, this test should be updated to fail loudly —
	// it exists as a tripwire for that refactor.
	//
	// The check here is behavioural: Send must reject a body that is not
	// a serialised ToolCall, so it cannot be repurposed as a general
	// "send this text" door.
	reg := NewRegistry()
	c, _ := newFakeServer(t, defaultHandler)
	mustInit(t, c)
	if err := reg.Add("w", c); err != nil {
		t.Fatal(err)
	}

	_, err := reg.Send(context.Background(), bus.Worker{Name: "w", Destination: privacy.Local},
		"just some text, not a tool call")
	if err == nil || !strings.Contains(err.Error(), "not a ToolCall") {
		t.Fatalf("err = %v, want a refusal to treat arbitrary text as a call", err)
	}
}
