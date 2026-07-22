package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ear "github.com/qaflabindia/ear"
)

// cmdMonitor renders the fleet's health as a terminal frame: one row per
// instance, worst-first, drawn from the reasoning trails. It reads a directory
// of JSONL trails (one instance per file) so an operator can look at a fleet
// without a running server.
//
// It is the same distilled health model the /fleet endpoint serves, so the
// control-room wall and the browser board never disagree about whether an
// instance is well.
func cmdMonitor(args []string) int {
	flags := flag.NewFlagSet("ear monitor", flag.ContinueOnError)
	flagArgs, positionals := reorderArgs(args, map[string]bool{})
	if err := flags.Parse(flagArgs); err != nil {
		return exitError
	}
	if len(positionals) != 1 {
		fmt.Fprintln(os.Stderr, "usage: ear monitor <trails-dir>")
		fmt.Fprintln(os.Stderr, "  a directory of .jsonl reasoning trails, one per instance")
		return exitError
	}

	fleet, err := fleetFromTrails(positionals[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear monitor:", err)
		return exitError
	}
	fmt.Print(ear.RenderFleet(fleet))

	// A broken chain anywhere is the one condition an operator must not miss,
	// so it is also the exit code -- a monitor in a health check can branch on
	// it without parsing the frame.
	if fleet.Broken > 0 {
		return exitBlocked
	}
	return exitDecided
}

// fleetFromTrails builds a fleet-health view from a directory of JSONL trails,
// each file an instance named by its stem. It reuses the package's
// InspectTrailFile, which reads the trail and verifies its chain with the
// file-canonical VerifyTrail -- one definition of health, whatever the source.
func fleetFromTrails(dir string) (ear.FleetHealth, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ear.FleetHealth{}, fmt.Errorf("%q is not a directory of trails", dir)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return ear.FleetHealth{}, err
	}
	if len(matches) == 0 {
		return ear.FleetHealth{}, fmt.Errorf("no .jsonl trails in %q", dir)
	}
	sort.Strings(matches)

	now := time.Now()
	fleet := ear.FleetHealth{At: now}
	for _, path := range matches {
		name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		instance, err := ear.InspectTrailFile(name, path, now)
		if err != nil {
			return ear.FleetHealth{}, fmt.Errorf("reading %s: %w", filepath.Base(path), err)
		}
		fleet.Add(instance)
	}
	fleet.SortWorstFirst()
	return fleet, nil
}
