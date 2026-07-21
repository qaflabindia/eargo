package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	ear "github.com/qaflabindia/ear"
)

// cmdServe runs the control plane as a service: the Kernel scheduling standing
// work behind an HTTP front door that creates instances, submits intents and
// reports how the fleet is doing.
func cmdServe(args []string) int {
	flags := flag.NewFlagSet("ear serve", flag.ContinueOnError)
	addr := flags.String("addr", ":8080", "listen address")
	stacksRoot := flags.String("stacks", "", "directory stacks may be loaded from (required to create instances over the wire)")
	subject := flags.String("subject", "", "act as this identity (requires -org)")
	orgs := flags.String("org", "", "comma-separated org ids the identity may act as")
	workers := flags.Int("workers", 1, "how many different instances may run concurrently (-1 = one per CPU)")

	flagArgs, positionals := reorderArgs(args, map[string]bool{
		"addr": true, "stacks": true, "subject": true, "org": true, "workers": true,
	})
	if err := flags.Parse(flagArgs); err != nil {
		return exitError
	}

	claim, err := parseClaim(*subject, *orgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear serve:", err)
		return exitError
	}

	kernel := &ear.Kernel{Workers: *workers}
	server := ear.NewServer(kernel)
	server.Addr = *addr
	server.StacksRoot = *stacksRoot
	server.Claim = claim

	// Any stack directories named on the command line are registered up front
	// and their authored schedules armed, so a server can be started with its
	// fleet already running rather than requiring calls to populate it.
	for _, dir := range positionals {
		name := instanceName(dir)
		runtime, err := loadStack(dir, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ear serve:", err)
			return exitError
		}
		defer runtime.Close()

		kernel.Register(name, runtime)
		tasks, err := kernel.Arm(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ear serve:", err)
			return exitError
		}
		fmt.Printf("registered %-24s %s\n", name, describeSchedule(tasks))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Printf("\near serve -- listening on %s", *addr)
	if *stacksRoot != "" {
		if abs, err := filepath.Abs(*stacksRoot); err == nil {
			fmt.Printf(", stacks under %s", abs)
		}
	}
	if claim != nil {
		fmt.Printf(", acting as %s for %s", claim.Subject, strings.Join(claim.OrgIDs, ", "))
	}
	fmt.Print(". Ctrl-C to stop.\n\n")

	if err := server.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "ear serve:", err)
		return exitError
	}
	fmt.Println("\near serve -- stopped")
	return exitDecided
}
