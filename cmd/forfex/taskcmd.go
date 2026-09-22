package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/forfex-lab/forfex/internal/bus"
	"github.com/forfex-lab/forfex/internal/mcp"
	"github.com/forfex-lab/forfex/internal/task"
)

func taskCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, taskUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return taskListCmd(stdout)
	case "explain":
		return taskExplainCmd(args[1:], stdout, stderr)
	case "verify":
		return taskVerifyCmd(args[1:], stdout, stderr)
	case "run":
		return taskRunCmd(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, taskUsage)
		return 0
	}
	fmt.Fprintf(stderr, "forfex task: unknown subcommand %q\n", args[0])
	fmt.Fprint(stderr, taskUsage)
	return 2
}

const taskUsage = `Usage:
  forfex task list
  forfex task explain -task NAME [-args JSON] [-destination local|external]
  forfex task verify  [-worker NAME] -- <server-cmd> [args...]
  forfex task run     -task NAME [-args JSON] -destination local|external
                      -- <server-cmd> [args...]

list     Print the catalogue: each task's tool, and each argument's
         declared provenance.
explain  Derive the provenance of one call and say whether Rule 4 would
         permit it. Starts no server and sends nothing.
verify   Compare the catalogue's declared arguments against the schemas
         a live server publishes. Reports verified, unverifiable and
         problem counts separately, so a run that checked nothing cannot
         read as a run that found nothing wrong.
run      Run a task through the bus.

A task declares its arguments and the provenance of each. Unlike
` + "`forfex call`" + `, the provenance is derived from what was actually
supplied rather than asserted on the command line, and an argument the
task does not declare is refused before anything is sent.

Exit 0 ok, 1 refused or tool error, 2 usage.
`

func taskListCmd(stdout io.Writer) int {
	for _, t := range catalog().All() {
		fmt.Fprintf(stdout, "%s\n    %s\n    %s on worker %q\n",
			t.Name, t.Summary, t.Tool, t.Worker)
		fmt.Fprintf(stdout, "    baseline provenance: %s\n", t.Baseline.Describe())
		for _, in := range t.Inputs {
			req := "optional"
			if in.Required {
				req = "required"
			}
			fmt.Fprintf(stdout, "      %-12s %-8s %-36s %s\n",
				in.Name, req, in.Provenance.Describe(), in.Summary)
		}
	}
	return 0
}

func taskExplainCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("task explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("task", "", "task name")
	argsJSON := fs.String("args", "{}", "arguments as a JSON object")
	dest := fs.String("destination", "external", "local or external")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(stderr, "forfex task explain: -task is required")
		return 2
	}

	t, ok := catalog().Lookup(*name)
	if !ok {
		fmt.Fprintf(stderr, "forfex task explain: %v: %q\n", task.ErrUnknownTask, *name)
		return 2
	}
	d, err := parseDestination(*dest)
	if err != nil {
		fmt.Fprintf(stderr, "forfex task explain: %v\n", err)
		return 2
	}
	toolArgs, err := parseArgs(*argsJSON)
	if err != nil {
		fmt.Fprintf(stderr, "forfex task explain: %v\n", err)
		return 2
	}

	if err := t.CheckArgs(toolArgs); err != nil {
		fmt.Fprintf(stderr, "forfex task explain: %v\n", err)
		return 2
	}
	prov, from := t.Effective(toolArgs)

	fmt.Fprintf(stdout, "task        %s\n", t.Name)
	fmt.Fprintf(stdout, "worker      %s -> %s\n", t.Worker, d)
	origin := "baseline"
	if from != "" {
		origin = fmt.Sprintf("from input %q", from)
	}
	fmt.Fprintf(stdout, "provenance  %s (%s)\n", prov.Describe(), origin)

	// A nil transport makes this subcommand structurally incapable of
	// sending, the same way `forfex route` is. If policy says yes,
	// Dispatch stops at the missing transport.
	b := bus.New(nil, nil)
	if err := b.Register(bus.Worker{Name: t.Worker, Destination: d}); err != nil {
		fmt.Fprintf(stderr, "forfex task explain: %v\n", err)
		return 2
	}
	_, err = task.Run(context.Background(), b, t, toolArgs)

	switch {
	case errors.Is(err, bus.ErrRefused):
		fmt.Fprintf(stdout, "result      %v\n", err)
		return 1
	case errors.Is(err, bus.ErrNoTransport):
		fmt.Fprintln(stdout, "result      allowed (nothing was sent)")
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "forfex task explain: %v\n", err)
		return 2
	default:
		// Unreachable with a nil transport, and saying so beats a
		// silent success that would mean the nil is gone.
		fmt.Fprintln(stderr, "forfex task explain: dispatched with no transport")
		return 2
	}
}

func taskVerifyCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("task verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worker := fs.String("worker", "vault", "check the tasks declared for this worker")
	timeout := fs.Duration("timeout", 30*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := startServer(ctx, stderr, fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "forfex task verify: %v\n", err)
		return 2
	}
	defer c.Close()

	tools, err := c.ListTools(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "forfex task verify: %v\n", err)
		return 2
	}

	rep := task.VerifyAgainst(catalog().All(), *worker, tools)
	for _, n := range rep.Verified {
		fmt.Fprintf(stdout, "ok            %s\n", n)
	}
	for _, l := range rep.Unverifiable {
		fmt.Fprintf(stdout, "unverifiable  %s\n", l)
	}
	for _, l := range rep.Problems {
		fmt.Fprintf(stdout, "PROBLEM       %s\n", l)
	}
	fmt.Fprintf(stdout, "%s (worker %q, %d tools offered)\n",
		rep.Summary(), *worker, len(tools))

	if !rep.OK() {
		return 1
	}
	return 0
}

func taskRunCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("task run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("task", "", "task name")
	argsJSON := fs.String("args", "{}", "arguments as a JSON object")
	dest := fs.String("destination", "", "local or external")
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || *dest == "" {
		fmt.Fprintln(stderr, "forfex task run: -task and -destination are required")
		return 2
	}

	t, ok := catalog().Lookup(*name)
	if !ok {
		fmt.Fprintf(stderr, "forfex task run: %v: %q\n", task.ErrUnknownTask, *name)
		return 2
	}
	d, err := parseDestination(*dest)
	if err != nil {
		fmt.Fprintf(stderr, "forfex task run: %v\n", err)
		return 2
	}
	toolArgs, err := parseArgs(*argsJSON)
	if err != nil {
		fmt.Fprintf(stderr, "forfex task run: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	c, err := startServer(ctx, stderr, fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "forfex task run: %v\n", err)
		return 2
	}
	defer c.Close()

	// The worker name comes from the task, not the command line: a task
	// declares which worker it runs on, and letting a flag override that
	// would let a caller point a declaration at a different container.
	reg := mcp.NewRegistry()
	if err := reg.Add(t.Worker, c); err != nil {
		fmt.Fprintf(stderr, "forfex task run: %v\n", err)
		return 2
	}
	b := bus.New(reg, nil)
	if err := b.Register(bus.Worker{Name: t.Worker, Destination: d}); err != nil {
		fmt.Fprintf(stderr, "forfex task run: %v\n", err)
		return 2
	}

	res, err := task.Run(ctx, b, t, toolArgs)
	switch {
	case errors.Is(err, bus.ErrRefused):
		// Names the task and the input responsible, and no values.
		prov, from := t.Effective(toolArgs)
		origin := "the task baseline"
		if from != "" {
			origin = fmt.Sprintf("input %q", from)
		}
		fmt.Fprintf(stdout, "%v\n  task %s derived %s from %s\n",
			err, t.Name, prov.Describe(), origin)
		return 1
	case errors.Is(err, task.ErrUndeclaredArgument),
		errors.Is(err, task.ErrMissingArgument),
		errors.Is(err, task.ErrInvalidTask):
		fmt.Fprintf(stderr, "forfex task run: %v\n", err)
		return 2
	case err != nil:
		fmt.Fprintf(stderr, "forfex task run: %v\n", err)
		return 2
	}

	if res.IsError {
		fmt.Fprintf(stdout, "tool reported an error:\n%s\n", res.Text())
		return 1
	}
	fmt.Fprintln(stdout, res.Text())
	return 0
}

func parseArgs(s string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("-args is not a JSON object: %w", err)
	}
	return m, nil
}
