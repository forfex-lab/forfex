package bus_test

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rule 4 says a worker must be structurally unable to receive restricted
// sequence. The bus makes that true for anything that goes through the
// bus — but "goes through the bus" is a convention until something
// checks it. A package that opens its own socket or spawns its own
// subprocess has left the building by a side door, and no amount of
// care inside Dispatch would notice.
//
// So: only the packages named below may import something that can leave
// this machine. Everything else is a compile-time-adjacent guarantee
// that the only exit is Dispatch.
//
// When this test fails the answer is usually to move the egress into a
// transport, not to widen the allowlist. Widen it deliberately, in a
// commit that says why.

// egressPackages can reach off-process. The list is the threat model.
var egressPackages = map[string]string{
	"net":              "raw sockets",
	"net/http":         "HTTP",
	"os/exec":          "subprocesses — an MCP stdio server is one of these",
	"net/rpc":          "RPC",
	"net/smtp":         "mail",
	"net/url":          "", // parsing only; harmless, listed so it is a conscious pass
	"golang.org/x/net": "extended net",
}

// allowed names the packages permitted to import them, by import path
// suffix relative to the module root.
var allowed = map[string]bool{
	"internal/bus":       true, // defines Transport; may hold an implementation
	"internal/transport": true, // where real transports will live
	"internal/mcp":       true, // the MCP client, when it exists (CGTRU-18)
}

func TestOnlyTransportPackagesCanReachOffMachine(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	var violations []string

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == ".git" || base == "testdata" || base == "vendor" {
			return filepath.SkipDir
		}

		pkg, err := build.ImportDir(path, 0)
		if err != nil {
			return nil // not a Go package directory
		}

		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if allowed[rel] {
			return nil
		}

		for _, imp := range append(pkg.Imports, pkg.TestImports...) {
			why, listed := egressPackages[imp]
			if !listed || why == "" {
				continue
			}
			violations = append(violations,
				rel+" imports "+imp+" ("+why+")")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(violations) > 0 {
		t.Fatalf("packages outside the transport allowlist can reach off-machine:\n  %s\n\n"+
			"Rule 4 is only enforceable if Dispatch is the single exit. Move the egress\n"+
			"into a transport, or widen `allowed` deliberately and say why in the commit.",
			strings.Join(violations, "\n  "))
	}
}

// TestGuardIsNotVacuous proves the walk actually inspects packages,
// so a silent failure to find any package cannot pass as a clean run.
func TestGuardIsNotVacuous(t *testing.T) {
	root, _ := filepath.Abs("../..")
	found := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if base := filepath.Base(path); base == ".git" {
			return filepath.SkipDir
		}
		if _, err := build.ImportDir(path, 0); err == nil {
			found++
		}
		return nil
	})
	if found < 3 {
		t.Fatalf("walked the module and found %d Go packages; expected at least 3 "+
			"(cmd/forfex, internal/bus, internal/privacy). The guard above is not looking at anything.", found)
	}
}
