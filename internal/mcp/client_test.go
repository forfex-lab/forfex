package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is an in-process MCP server over pipes. handler sees each
// decoded request and returns the raw `result` JSON, or an rpcError.
type fakeServer struct {
	t       *testing.T
	handler func(method string, params json.RawMessage) (any, *rpcError)

	mu       sync.Mutex
	methods  []string
	rawLines []string
}

func newFakeServer(t *testing.T, h func(string, json.RawMessage) (any, *rpcError)) (*Client, *fakeServer) {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()

	fs := &fakeServer{t: t, handler: h}
	go fs.serve(serverR, serverW)

	c := NewClient(clientR, clientW, clientW)
	t.Cleanup(func() { _ = clientW.Close() })
	return c, fs
}

func (f *fakeServer) serve(r io.Reader, w io.WriteCloser) {
	defer w.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Text()

		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		f.mu.Lock()
		f.methods = append(f.methods, req.Method)
		f.rawLines = append(f.rawLines, line)
		f.mu.Unlock()

		if req.ID == nil { // a notification; no reply
			continue
		}

		result, rerr := f.handler(req.Method, req.Params)
		resp := map[string]any{"jsonrpc": "2.0", "id": *req.ID}
		if rerr != nil {
			resp["error"] = rerr
		} else {
			resp["result"] = result
		}
		b, _ := json.Marshal(resp)
		if _, err := w.Write(append(b, '\n')); err != nil {
			return
		}
	}
}

func (f *fakeServer) sawMethods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

func (f *fakeServer) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rawLines...)
}

func defaultHandler(method string, _ json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return initializeResult{
			ProtocolVersion: ProtocolVersion,
			ServerInfo:      ServerInfo{Name: "fake", Version: "0.1"},
		}, nil
	case "tools/list":
		return listToolsResult{Tools: []Tool{{Name: "echo", Description: "echoes"}}}, nil
	case "tools/call":
		return ToolResult{Content: []Content{{Type: "text", Text: "done"}}}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found"}
}

func mustInit(t *testing.T, c *Client) {
	t.Helper()
	if _, err := c.Initialize(context.Background(), "forfex-test", "0"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

func TestInitializeHandshake(t *testing.T) {
	c, fs := newFakeServer(t, defaultHandler)

	info, err := c.Initialize(context.Background(), "forfex", "1.2.3")
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.Name != "fake" || info.Version != "0.1" {
		t.Errorf("ServerInfo = %+v", info)
	}
	if got := c.NegotiatedProtocol(); got != ProtocolVersion {
		t.Errorf("NegotiatedProtocol = %q", got)
	}

	// The spec requires the notification after the response. Skipping it
	// leaves some servers refusing every subsequent call, which shows up
	// as a confusing "not initialized" far from the cause.
	//
	// Polled rather than read once: the pipe write returns when the
	// server's scanner consumes the bytes, which is before its handler
	// has recorded the method. Reading immediately is a race that fails
	// about as often as it passes.
	want := []string{"initialize", "notifications/initialized"}
	var got []string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if got = fs.sawMethods(); len(got) >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("server saw %v, want %v", got, want)
	}

	// clientInfo must actually carry through; a server may log or gate on it.
	if !strings.Contains(fs.lines()[0], `"name":"forfex"`) {
		t.Errorf("initialize did not carry clientInfo: %s", fs.lines()[0])
	}
}

func TestServerMayNegotiateADifferentProtocolVersion(t *testing.T) {
	// The spec lets a server answer with a version it supports. Refusing
	// to talk to it would be stricter than the spec; recording what it
	// said is what lets a human notice drift later.
	c, _ := newFakeServer(t, func(m string, p json.RawMessage) (any, *rpcError) {
		if m == "initialize" {
			return initializeResult{
				ProtocolVersion: "2024-11-05",
				ServerInfo:      ServerInfo{Name: "old", Version: "0.0"},
			}, nil
		}
		return defaultHandler(m, p)
	})

	if _, err := c.Initialize(context.Background(), "forfex", "1"); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := c.NegotiatedProtocol(); got != "2024-11-05" {
		t.Errorf("NegotiatedProtocol = %q, want the server's answer", got)
	}
}

func TestCallsBeforeInitializeAreRefused(t *testing.T) {
	c, _ := newFakeServer(t, defaultHandler)

	if _, err := c.ListTools(context.Background()); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("ListTools err = %v, want ErrNotInitialized", err)
	}
	if _, err := c.CallTool(context.Background(), "echo", nil); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("CallTool err = %v, want ErrNotInitialized", err)
	}
}

func TestListToolsFollowsPagination(t *testing.T) {
	// A server that pages and a client that reads only the first page
	// produce a tool list that is quietly short — capability discovery
	// that discovers some capabilities.
	page := 0
	c, _ := newFakeServer(t, func(m string, p json.RawMessage) (any, *rpcError) {
		if m != "tools/list" {
			return defaultHandler(m, p)
		}
		page++
		switch page {
		case 1:
			return listToolsResult{Tools: []Tool{{Name: "a"}}, NextCursor: "c1"}, nil
		case 2:
			return listToolsResult{Tools: []Tool{{Name: "b"}}, NextCursor: "c2"}, nil
		default:
			return listToolsResult{Tools: []Tool{{Name: "c"}}}, nil
		}
	})
	mustInit(t, c)

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("got %d tools, want 3 across three pages", len(tools))
	}
}

func TestListToolsRefusesARepeatingCursor(t *testing.T) {
	c, _ := newFakeServer(t, func(m string, p json.RawMessage) (any, *rpcError) {
		if m == "tools/list" {
			return listToolsResult{Tools: []Tool{{Name: "a"}}, NextCursor: "same"}, nil
		}
		return defaultHandler(m, p)
	})
	mustInit(t, c)

	done := make(chan error, 1)
	go func() { _, err := c.ListTools(context.Background()); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a self-referential cursor was followed without complaint")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListTools spun on a repeating cursor instead of stopping")
	}
}

func TestCallToolSuccessAndToolLevelFailure(t *testing.T) {
	c, _ := newFakeServer(t, func(m string, p json.RawMessage) (any, *rpcError) {
		if m == "tools/call" {
			var args struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(p, &args)
			if args.Name == "broken" {
				return ToolResult{
					Content: []Content{{Type: "text", Text: "exit status 1"}},
					IsError: true,
				}, nil
			}
			return ToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
		}
		return defaultHandler(m, p)
	})
	mustInit(t, c)

	res, err := c.CallTool(context.Background(), "echo", map[string]any{"x": 1})
	if err != nil || res.IsError || res.Text() != "ok" {
		t.Fatalf("good call: res=%+v err=%v", res, err)
	}

	// A tool that ran and failed is a RESULT, not a transport error.
	// Conflating them makes a deterministic failure look retryable.
	res, err = c.CallTool(context.Background(), "broken", nil)
	if err != nil {
		t.Fatalf("a tool-level failure must not be a Go error: %v", err)
	}
	if !res.IsError || res.Text() != "exit status 1" {
		t.Fatalf("res = %+v, want IsError with the message", res)
	}
}

func TestJSONRPCErrorIsReturned(t *testing.T) {
	c, _ := newFakeServer(t, func(m string, p json.RawMessage) (any, *rpcError) {
		if m == "tools/call" {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		return defaultHandler(m, p)
	})
	mustInit(t, c)

	if _, err := c.CallTool(context.Background(), "echo", nil); err == nil ||
		!strings.Contains(err.Error(), "invalid params") {
		t.Fatalf("err = %v, want the JSON-RPC error surfaced", err)
	}
}

func TestInterleavedNotificationsAreSkipped(t *testing.T) {
	// A server may send progress notifications and its own requests
	// while ours is in flight. Treating the first line that arrives as
	// the answer would decode a notification into the result.
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	c := NewClient(clientR, clientW, clientW)

	go func() {
		defer serverW.Close()
		sc := bufio.NewScanner(serverR)
		for sc.Scan() {
			var req struct {
				ID     *int64 `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(sc.Bytes(), &req)
			if req.ID == nil {
				continue
			}
			// Noise first: a notification, then an unrelated id.
			_, _ = serverW.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}` + "\n"))
			_, _ = serverW.Write([]byte(`{"jsonrpc":"2.0","id":9999,"result":{"protocolVersion":"wrong"}}` + "\n"))

			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": *req.ID,
				"result": initializeResult{ProtocolVersion: ProtocolVersion, ServerInfo: ServerInfo{Name: "fake"}},
			})
			_, _ = serverW.Write(append(resp, '\n'))
		}
	}()

	info, err := c.Initialize(context.Background(), "forfex", "1")
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.Name != "fake" || c.NegotiatedProtocol() != ProtocolVersion {
		t.Fatalf("client used an interleaved message as its answer: %+v / %q", info, c.NegotiatedProtocol())
	}
}

func TestOversizedMessageIsRefusedNotBuffered(t *testing.T) {
	clientR, serverW := io.Pipe()
	drain, clientW := io.Pipe()
	// The request has to go somewhere or ListTools blocks on the write
	// and never reaches the read this test is about.
	go func() { _, _ = io.Copy(io.Discard, drain) }()
	c := NewClient(clientR, clientW, clientW)
	c.initialized = true

	go func() {
		// One very long line with no newline in sight.
		chunk := strings.Repeat("x", 1<<20)
		for i := 0; i < 16; i++ {
			if _, err := serverW.Write([]byte(chunk)); err != nil {
				return
			}
		}
		serverW.Close()
	}()

	done := make(chan error, 1)
	go func() { _, err := c.ListTools(context.Background()); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an unbounded message was accepted")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("client hung buffering an unbounded message")
	}
}

func TestToolResultTextJoinsTextBlocksOnly(t *testing.T) {
	r := ToolResult{Content: []Content{
		{Type: "text", Text: "first"},
		{Type: "image", Text: "ignored"},
		{Type: "text", Text: "second"},
	}}
	if got := r.Text(); got != "first\nsecond" {
		t.Errorf("Text() = %q, want %q", got, "first\nsecond")
	}
}
