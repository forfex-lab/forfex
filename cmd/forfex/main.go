// Command forfex is the orchestrator entry point.
//
// It currently does one thing: `forfex route` answers whether a payload
// would be allowed to reach a named worker, without sending it. That is
// a small subcommand, but it is the one that makes Rule 4 checkable by
// hand rather than only by test — and it means the privacy and bus
// packages have a caller from the binary rather than from _test.go
// alone.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/forfex-lab/forfex/internal/bus"
	"github.com/forfex-lab/forfex/internal/privacy"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "route":
		os.Exit(routeCmd(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "forfex: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `forfex — programmable-nuclease experiment design orchestrator

Usage:
  forfex route -worker NAME -destination local|external [-provenance P] < body

Reads a payload body on stdin and reports whether Rule 4 permits sending
it to that worker. Nothing is sent. Exit 0 allowed, 1 refused, 2 usage.

  -provenance  published | unpublished | elabftw | unknown  (default unknown)

The body is never echoed, so this is safe to run on material that must
not be logged.
`)
}

// routeCmd is separated from main so it can be tested with ordinary
// readers and writers.
func routeCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	fs.SetOutput(stderr)
	worker := fs.String("worker", "", "worker name")
	dest := fs.String("destination", "", "local or external")
	prov := fs.String("provenance", "unknown", "published, unpublished, elabftw or unknown")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *worker == "" || *dest == "" {
		fmt.Fprintln(stderr, "forfex route: -worker and -destination are required")
		return 2
	}

	d, err := parseDestination(*dest)
	if err != nil {
		fmt.Fprintf(stderr, "forfex route: %v\n", err)
		return 2
	}
	p, err := parseProvenance(*prov)
	if err != nil {
		fmt.Fprintf(stderr, "forfex route: %v\n", err)
		return 2
	}

	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "forfex route: reading stdin: %v\n", err)
		return 2
	}

	// A nil transport is deliberate: this subcommand must be incapable
	// of sending. If policy ever says yes, Dispatch stops at the
	// missing transport rather than delivering anything.
	b := bus.New(nil, nil)
	if err := b.Register(bus.Worker{Name: *worker, Destination: d}); err != nil {
		fmt.Fprintf(stderr, "forfex route: %v\n", err)
		return 2
	}

	_, err = b.Dispatch(context.Background(), *worker,
		privacy.Payload{Provenance: p, Body: string(body)})

	switch {
	case errors.Is(err, bus.ErrRefused):
		fmt.Fprintf(stdout, "%v\n", err)
		return 1
	case errors.Is(err, bus.ErrNoTransport):
		// Policy allowed it; there is simply nowhere to send from here.
		fmt.Fprintf(stdout, "allowed: %s -> %s\n", p.Describe(), d.String())
		return 0
	case err != nil:
		fmt.Fprintf(stderr, "forfex route: %v\n", err)
		return 2
	default:
		fmt.Fprintf(stdout, "allowed: %s -> %s\n", p.Describe(), d.String())
		return 0
	}
}

func parseDestination(s string) (privacy.Destination, error) {
	switch s {
	case "local":
		return privacy.Local, nil
	case "external":
		return privacy.External, nil
	}
	// No default. Guessing here would be guessing in the unsafe
	// direction half the time.
	return privacy.External, fmt.Errorf("destination must be local or external, got %q", s)
}

func parseProvenance(s string) (privacy.Provenance, error) {
	switch s {
	case "published":
		return privacy.Published, nil
	case "unpublished":
		return privacy.Unpublished, nil
	case "elabftw":
		return privacy.ELabFTW, nil
	case "unknown", "":
		return privacy.Unknown, nil
	}
	return privacy.Unknown, fmt.Errorf("unrecognised provenance %q", s)
}
