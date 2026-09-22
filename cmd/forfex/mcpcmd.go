package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/forfex-lab/forfex/internal/bus"
	"github.com/forfex-lab/forfex/internal/mcp"
)

// startServer launches an MCP server subprocess and completes the
// handshake. Shared by `tools` and `call`.
func startServer(ctx context.Context, stderr io.Writer, argv []string) (*mcp.Client, error) {
	if len(argv) == 0 {
		return nil, errors.New("no server command given; put it after --")
	}
	c, err := mcp.StdioProcess(ctx, stderr, argv[0], argv[1:]...)
	if err != nil {
		return nil, err
	}
	if _, err := c.Initialize(ctx, "forfex", "0.1"); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// toolsCmd is capability discovery, visible from a shell.
func toolsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", 30*time.Second, "overall timeout")
	// Authoring a task means declaring the tool's arguments by name, and
	// guessing them from a description is how a declaration drifts from
	// the tool it describes. `forfex task verify` catches that drift
	// afterwards; this flag is how you avoid introducing it.
	schemas := fs.Bool("schemas", false, "also print each tool's inputSchema")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := startServer(ctx, stderr, fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "forfex tools: %v\n", err)
		return 2
	}
	defer c.Close()

	info := c.ServerInfo()
	fmt.Fprintf(stdout, "%s %s (protocol %s)\n", info.Name, info.Version, c.NegotiatedProtocol())

	tools, err := c.ListTools(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "forfex tools: %v\n", err)
		return 2
	}
	if len(tools) == 0 {
		fmt.Fprintln(stdout, "(no tools)")
		return 0
	}
	for _, t := range tools {
		fmt.Fprintf(stdout, "  %-28s %s\n", t.Name, t.Description)
		if !*schemas {
			continue
		}
		if len(t.InputSchema) == 0 {
			fmt.Fprintln(stdout, "      (no inputSchema published)")
			continue
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, t.InputSchema, "      ", "  "); err != nil {
			// Print it raw rather than dropping it: an unparseable
			// schema is something the task author needs to see.
			fmt.Fprintf(stdout, "      %s\n", t.InputSchema)
			continue
		}
		fmt.Fprintf(stdout, "      %s\n", pretty.String())
	}
	return 0
}

// callCmd invokes a tool THROUGH THE BUS, so Rule 4 applies exactly as
// it would in the orchestrator. This is the end-to-end demonstration
// that the boundary is real: a restricted payload is refused here for
// the same reason and by the same code.
func callCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tool := fs.String("tool", "", "tool name")
	argsJSON := fs.String("args", "{}", "tool arguments as a JSON object")
	dest := fs.String("destination", "", "local or external")
	prov := fs.String("provenance", "unknown", "published, unpublished, elabftw or unknown")
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tool == "" || *dest == "" {
		fmt.Fprintln(stderr, "forfex call: -tool and -destination are required")
		return 2
	}

	d, err := parseDestination(*dest)
	if err != nil {
		fmt.Fprintf(stderr, "forfex call: %v\n", err)
		return 2
	}
	p, err := parseProvenance(*prov)
	if err != nil {
		fmt.Fprintf(stderr, "forfex call: %v\n", err)
		return 2
	}

	var toolArgs map[string]any
	if err := json.Unmarshal([]byte(*argsJSON), &toolArgs); err != nil {
		fmt.Fprintf(stderr, "forfex call: -args is not a JSON object: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := startServer(ctx, stderr, fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "forfex call: %v\n", err)
		return 2
	}
	defer c.Close()

	const worker = "server"
	reg := mcp.NewRegistry()
	if err := reg.Add(worker, c); err != nil {
		fmt.Fprintf(stderr, "forfex call: %v\n", err)
		return 2
	}
	b := bus.New(reg, nil)
	if err := b.Register(bus.Worker{Name: worker, Destination: d}); err != nil {
		fmt.Fprintf(stderr, "forfex call: %v\n", err)
		return 2
	}

	res, err := mcp.Dispatch(ctx, b, worker,
		mcp.ToolCall{Tool: *tool, Arguments: toolArgs}, p)

	switch {
	case errors.Is(err, bus.ErrRefused):
		// Deliberately does not echo the arguments.
		fmt.Fprintf(stdout, "%v\n", err)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "forfex call: %v\n", err)
		return 2
	}

	if res.IsError {
		fmt.Fprintf(stdout, "tool reported an error:\n%s\n", res.Text())
		return 1
	}
	fmt.Fprintln(stdout, res.Text())
	return 0
}
