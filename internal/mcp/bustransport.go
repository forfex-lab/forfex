package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/forfex-lab/forfex/internal/bus"
	"github.com/forfex-lab/forfex/internal/privacy"
)

// ToolCall is one invocation, as it travels across the bus.
//
// The whole call — tool name AND every argument — is serialised into
// the bus payload body before the privacy check runs. That is the
// point: privacy.Check scans the serialised JSON, so a nucleotide run
// is found in ANY argument, whatever the server chose to call that
// field. A design that checked only a field named "sequence" would be
// guessing at someone else's schema and would miss the first server
// that names it "query".
//
// The known gap is unchanged and worth restating: an argument whose
// value is base64 or otherwise encoded is not decoded before scanning.
type ToolCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// Registry maps bus worker names to MCP clients.
//
// It implements bus.Transport, which means an MCP call can only be made
// through bus.Dispatch — there is no exported path from a ToolCall to a
// Client that skips the check.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]*Client
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{clients: make(map[string]*Client)}
}

// Add binds a client to a worker name. It refuses to replace an
// existing binding, for the same reason bus.Register does: a silent
// replacement could redirect a worker somewhere else.
func (r *Registry) Add(worker string, c *Client) error {
	if worker == "" {
		return fmt.Errorf("mcp: worker name must not be empty")
	}
	if c == nil {
		return fmt.Errorf("mcp: client for %q is nil", worker)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.clients[worker]; exists {
		return fmt.Errorf("mcp: worker %q already has a client", worker)
	}
	r.clients[worker] = c
	return nil
}

// Client returns the client bound to a worker, if any.
func (r *Registry) Client(worker string) (*Client, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[worker]
	return c, ok
}

// Close shuts every client down, returning the first error.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for _, c := range r.clients {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Send implements bus.Transport. The bus has already decided by the
// time this runs; this function must not re-decide, and must not be
// reachable except through Dispatch.
func (r *Registry) Send(ctx context.Context, w bus.Worker, body string) (string, error) {
	var call ToolCall
	if err := json.Unmarshal([]byte(body), &call); err != nil {
		return "", fmt.Errorf("mcp: payload is not a ToolCall: %w", err)
	}

	c, ok := r.Client(w.Name)
	if !ok {
		return "", fmt.Errorf("mcp: no client registered for worker %q", w.Name)
	}

	res, err := c.CallTool(ctx, call.Tool, call.Arguments)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("mcp: encoding tool result: %w", err)
	}
	return string(out), nil
}

// Dispatch is how the orchestrator calls a tool.
//
// It serialises the call, hands it to the bus — which applies Rule 4 —
// and decodes the result. Callers do not build the payload body
// themselves, so there is no opportunity to construct one that omits an
// argument from the scan.
func Dispatch(ctx context.Context, b *bus.Bus, worker string, call ToolCall, prov privacy.Provenance) (ToolResult, error) {
	body, err := json.Marshal(call)
	if err != nil {
		return ToolResult{}, fmt.Errorf("mcp: encoding tool call: %w", err)
	}

	out, err := b.Dispatch(ctx, worker, privacy.Payload{
		Provenance: prov,
		Body:       string(body),
	})
	if err != nil {
		return ToolResult{}, err
	}

	var res ToolResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return ToolResult{}, fmt.Errorf("mcp: decoding tool result: %w", err)
	}
	return res, nil
}
