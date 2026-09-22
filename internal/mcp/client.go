// Package mcp is a minimal MCP client for the orchestrator.
//
// It speaks the subset the orchestrator needs — initialize, tools/list,
// tools/call — over the stdio transport, which is JSON-RPC 2.0 with one
// message per line. Rule 6 puts every external bioinformatics tool in
// its own container reached over MCP, and a container's stdio is what
// this connects to.
//
// The client does NOT decide whether a payload may be sent. That is
// internal/bus, and the adapter in bustransport.go is how a tool call
// gets there. Nothing here should grow a code path that sends without
// passing through Dispatch.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ErrNotInitialized means a call was made before the handshake.
var ErrNotInitialized = errors.New("mcp: client not initialized")

// maxLine bounds a single JSON-RPC message. A server that streams an
// unbounded line would otherwise grow the orchestrator's heap until it
// dies, which is a denial of service by accident rather than malice.
const maxLine = 8 << 20 // 8 MiB

// Client is one connection to one MCP server.
//
// Safe for concurrent use: requests are serialised by mu. MCP allows
// pipelining by id, but serialising is simpler and the orchestrator's
// concurrency is across servers rather than within one.
type Client struct {
	mu   sync.Mutex
	w    io.Writer
	r    *bufio.Reader
	c    io.Closer
	next int64

	initialized bool
	server      ServerInfo
	protocol    string

	// stop tears down a subprocess, if this client owns one.
	stop func() error
}

// NewClient speaks to a server over an already-open pair of streams.
// Useful for tests and for a transport this package does not own.
func NewClient(r io.Reader, w io.Writer, closer io.Closer) *Client {
	br := bufio.NewReaderSize(r, 64<<10)
	return &Client{w: w, r: br, c: closer}
}

// StdioProcess starts cmd and speaks MCP over its stdin and stdout.
//
// The child's stderr is handed to logw rather than discarded — an MCP
// server that fails to start usually says why there, and swallowing it
// turns a clear error into "no response".
//
// logw must be safe to write from another goroutine until Close
// returns. os/exec copies a child's stderr on a goroutine of its own
// whenever the writer is not an *os.File, and it keeps copying until
// Wait completes. An *os.File — what the CLI passes — needs nothing;
// anything else needs its own synchronisation, and reading it while the
// child is alive is otherwise a data race. This package's own tests had
// exactly that race until CI started running with -race.
func StdioProcess(ctx context.Context, logw io.Writer, name string, args ...string) (*Client, error) {
	return StdioProcessEnv(ctx, logw, nil, name, args...)
}

// StdioProcessEnv is StdioProcess with extra environment entries, in
// the "K=V" form. A containerised server usually needs at least a data
// path or an API base, and passing them through the parent environment
// would leak them to every other child as well.
//
// Entries are appended to the parent environment, so a later duplicate
// wins — which is how os/exec resolves it.
func StdioProcessEnv(ctx context.Context, logw io.Writer, env []string, name string, args ...string) (*Client, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp: stdout pipe: %w", err)
	}
	if logw != nil {
		cmd.Stderr = logw
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: start %s: %w", name, err)
	}

	c := NewClient(stdout, stdin, stdin)
	c.stop = func() error {
		_ = stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			// A server that ignores a closed stdin gets killed rather
			// than leaking a process for the life of the orchestrator.
			_ = cmd.Process.Kill()
			return <-done
		}
	}
	return c, nil
}

// Close shuts the connection down.
func (c *Client) Close() error {
	if c.stop != nil {
		return c.stop()
	}
	if c.c != nil {
		return c.c.Close()
	}
	return nil
}

// ServerInfo reports what the server said at initialize.
func (c *Client) ServerInfo() ServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.server
}

// NegotiatedProtocol is the version the SERVER answered with, which may
// differ from ProtocolVersion. Recorded rather than asserted.
func (c *Client) NegotiatedProtocol() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.protocol
}

// Initialize performs the handshake and sends notifications/initialized.
func (c *Client) Initialize(ctx context.Context, clientName, clientVersion string) (ServerInfo, error) {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    clientName,
			"version": clientVersion,
		},
	}

	var res initializeResult
	if err := c.call(ctx, "initialize", params, &res); err != nil {
		return ServerInfo{}, err
	}

	c.mu.Lock()
	c.server = res.ServerInfo
	c.protocol = res.ProtocolVersion
	c.initialized = true
	c.mu.Unlock()

	// The spec requires this notification before normal operation.
	if err := c.notify(ctx, "notifications/initialized", nil); err != nil {
		return res.ServerInfo, fmt.Errorf("mcp: initialized notification: %w", err)
	}
	return res.ServerInfo, nil
}

// ListTools is capability discovery. It follows nextCursor to the end,
// so the caller gets every tool rather than the first page.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	if !c.ready() {
		return nil, ErrNotInitialized
	}

	var all []Tool
	cursor := ""
	for page := 0; ; page++ {
		if page > 1000 {
			// A server that returns a cursor pointing at itself would
			// otherwise spin forever.
			return nil, errors.New("mcp: tools/list did not terminate after 1000 pages")
		}
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res listToolsResult
		if err := c.call(ctx, "tools/list", params, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			return all, nil
		}
		if res.NextCursor == cursor {
			return nil, errors.New("mcp: tools/list repeated the same cursor")
		}
		cursor = res.NextCursor
	}
}

// CallTool invokes a tool.
//
// A tool that ran and failed comes back with IsError set and no Go
// error: that is the server reporting a result, not the call failing.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	if !c.ready() {
		return ToolResult{}, ErrNotInitialized
	}
	if args == nil {
		args = map[string]any{}
	}
	params := map[string]any{"name": name, "arguments": args}

	var res ToolResult
	if err := c.call(ctx, "tools/call", params, &res); err != nil {
		return ToolResult{}, err
	}
	return res, nil
}

func (c *Client) ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialized
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.next++
	id := c.next

	if err := c.writeJSON(request{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return err
	}

	// Read until the response with our id arrives. Notifications and
	// server-initiated requests may be interleaved; they are skipped
	// rather than treated as our answer.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp response
		if err := c.readJSON(&resp); err != nil {
			return fmt.Errorf("mcp: %s: %w", method, err)
		}
		if resp.ID == nil || *resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp: %s: %w", method, resp.Error)
		}
		if out == nil || len(resp.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("mcp: %s: decoding result: %w", method, err)
		}
		return nil
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeJSON(notification{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("mcp: encoding request: %w", err)
	}
	b = append(b, '\n')
	if _, err := c.w.Write(b); err != nil {
		return fmt.Errorf("mcp: writing request: %w", err)
	}
	return nil
}

func (c *Client) readJSON(v any) error {
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("decoding message: %w", err)
	}
	return nil
}

// readLine returns one newline-delimited message, refusing one that
// exceeds maxLine rather than buffering it.
func (c *Client) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := c.r.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, fmt.Errorf("message exceeds %d bytes", maxLine)
		}
		if !isPrefix {
			return buf, nil
		}
	}
}
