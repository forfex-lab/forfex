package mcp

import (
	"encoding/json"
	"fmt"
)

// MCP's stdio transport is JSON-RPC 2.0, one message per line, UTF-8.
// That is the whole framing: no Content-Length header, no chunking. A
// message may not contain a bare newline, which json.Marshal guarantees
// because it escapes them.
//
// These types are hand-written rather than taken from an SDK. Rule 2
// names "MCP shims" as write-freely, the module has no dependencies,
// and the subset the orchestrator needs is three methods. The cost is
// that protocol evolution is ours to track — see the decision record.

// ProtocolVersion is what the client offers at initialize.
//
// The server may answer with a different version it supports. We record
// what came back rather than asserting a match, because refusing to
// speak to a server that offered a workable alternative would be
// stricter than the spec and less useful than asking.
const ProtocolVersion = "2025-06-18"

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// notification is a request with no ID; the server must not answer it.
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("jsonrpc error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// ServerInfo is what a server reports at initialize.
type ServerInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ServerInfo      ServerInfo      `json:"serverInfo"`
	Instructions    string          `json:"instructions,omitempty"`
}

// Tool is one entry from tools/list.
//
// InputSchema stays raw. The orchestrator does not validate arguments
// against it — the server does that, and re-implementing a JSON Schema
// validator here to reject calls the server would reject anyway is work
// that buys a second opinion on someone else's contract.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type listToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// Content is one block of a tool result.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolResult is what tools/call returns.
//
// IsError is the protocol's own flag for "the tool ran and failed",
// which is a different thing from a transport or JSON-RPC error. A
// caller that conflates them will retry a deterministic tool failure
// forever.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Text joins the text blocks, which is what a worker usually wants.
func (r ToolResult) Text() string {
	var b []byte
	for i, c := range r.Content {
		if c.Type != "text" {
			continue
		}
		if i > 0 && len(b) > 0 {
			b = append(b, '\n')
		}
		b = append(b, c.Text...)
	}
	return string(b)
}
