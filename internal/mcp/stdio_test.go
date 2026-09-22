package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// StdioProcess is the path that will actually be used — Rule 6 puts
// every external tool in its own container, and a container's stdio is
// what the orchestrator talks to. The pipe-based tests above never
// exercise process startup, pipe wiring or teardown, so this one spawns
// a real child.
//
// The child is this test binary re-executed with an env var set, which
// avoids shipping a fixture in another language. Python would be the
// obvious alternative and is barred from the core by the project's own
// style rule.

const helperEnv = "FORFEX_MCP_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) != "" {
		runHelperServer()
		return
	}
	os.Exit(m.Run())
}

// runHelperServer is a minimal MCP server on stdin/stdout.
func runHelperServer() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	enc := json.NewEncoder(os.Stdout)

	// Prove stderr is plumbed: a server that dies usually explains
	// itself here, and a client that discards it reports "no response".
	fmt.Fprintln(os.Stderr, "helper: ready")

	for sc.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil || req.ID == nil {
			continue
		}

		resp := map[string]any{"jsonrpc": "2.0", "id": *req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = initializeResult{
				ProtocolVersion: ProtocolVersion,
				ServerInfo:      ServerInfo{Name: "helper", Version: "9.9"},
			}
		case "tools/list":
			resp["result"] = listToolsResult{Tools: []Tool{
				{Name: "upper", Description: "uppercases text"},
			}}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text, _ := p.Arguments["text"].(string)
			resp["result"] = ToolResult{Content: []Content{
				{Type: "text", Text: strings.ToUpper(text)},
			}}
		default:
			resp["error"] = &rpcError{Code: -32601, Message: "method not found"}
		}
		_ = enc.Encode(resp)
	}
}

// safeLog is a concurrency-safe stderr sink for the helper child.
//
// os/exec copies a child's stderr on its own goroutine whenever the
// writer is not an *os.File, and it keeps doing so until Wait returns.
// A strings.Builder read from the test goroutine while that copier is
// running is a data race — one the race detector finds and an ordinary
// `go test` does not, which is how it survived until CI turned -race on.
//
// This is not only a test concern: StdioProcess takes any io.Writer, so
// any caller passing an unsynchronised one has the same race. The
// constraint is now documented on StdioProcess; this type is what the
// tests use to honour it.
type safeLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *safeLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *safeLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func startHelper(t *testing.T, stderr *safeLog) *Client {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate test binary: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	c, err := StdioProcessEnv(ctx, stderr, []string{helperEnv + "=1"}, exe)
	if err != nil {
		t.Fatalf("StdioProcess: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestStdioProcessRoundTrip(t *testing.T) {
	var stderr safeLog
	c := startHelper(t, &stderr)

	info, err := c.Initialize(context.Background(), "forfex", "1")
	if err != nil {
		t.Fatalf("Initialize: %v (stderr: %q)", err, stderr.String())
	}
	if info.Name != "helper" || info.Version != "9.9" {
		t.Fatalf("ServerInfo = %+v", info)
	}

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "upper" {
		t.Fatalf("tools = %+v", tools)
	}

	res, err := c.CallTool(context.Background(), "upper", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Text() != "HELLO" {
		t.Fatalf("Text() = %q, want HELLO", res.Text())
	}
}

func TestStdioProcessCapturesChildStderr(t *testing.T) {
	var stderr safeLog
	c := startHelper(t, &stderr)
	if _, err := c.Initialize(context.Background(), "forfex", "1"); err != nil {
		t.Fatal(err)
	}
	// Give the child's first write a moment to land.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if strings.Contains(stderr.String(), "helper: ready") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("child stderr was not captured; got %q", stderr.String())
}

func TestStdioProcessCloseTerminatesTheChild(t *testing.T) {
	var stderr safeLog
	c := startHelper(t, &stderr)
	if _, err := c.Initialize(context.Background(), "forfex", "1"); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- c.Close() }()

	select {
	case <-done:
		// A child that ignores a closed stdin is killed after the
		// grace period; either way Close must return.
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return; a leaked child outlives the orchestrator")
	}
}

func TestStdioProcessReportsAMissingBinary(t *testing.T) {
	_, err := StdioProcess(context.Background(), nil, "forfex-no-such-binary-xyz")
	if err == nil {
		t.Fatal("starting a nonexistent binary returned no error")
	}
	if !strings.Contains(err.Error(), "forfex-no-such-binary-xyz") {
		t.Errorf("error should name the binary: %v", err)
	}
}
