package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	ear "github.com/qaflabindia/ear"
)

// cmdKernel runs one or more stacks as a persistent enterprise runtime: each
// stack directory becomes a named instance in the kernel's process table, its
// authored `## Scheduled Work` is armed, and the idle loop runs until the
// operator interrupts it.
//
// This is the difference between a library you call and a runtime the
// enterprise operates. Between firings the process sleeps -- no busy-wait --
// and every firing goes through the instance's normal governed cycle, so the
// policies, the tenant boundary, the audit trail and the session store all
// apply exactly as they do to `ear run`.
func cmdKernel(args []string) int {
	flags := flag.NewFlagSet("ear kernel", flag.ContinueOnError)
	workers := flags.Int("workers", 1, "how many different instances may run concurrently (-1 = one per CPU)")
	subject := flags.String("subject", "", "act as this identity (requires -org)")
	orgs := flags.String("org", "", "comma-separated org ids the identity may act as")
	once := flags.Bool("once", false, "arm and drain the due work once, then exit (no idle loop)")
	asJSON := flags.Bool("json", false, "machine output: one JSON object per dispatch on stdout")
	statusEvery := flags.Duration("status-every", 0, "print a process-table snapshot on this period")

	flagArgs, positionals := reorderArgs(args, map[string]bool{
		"workers": true, "subject": true, "org": true, "status-every": true,
	})
	if err := flags.Parse(flagArgs); err != nil {
		return exitError
	}
	if len(positionals) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ear kernel <stack-dir>... [flags]")
		return exitError
	}

	claim, err := parseClaim(*subject, *orgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear kernel:", err)
		return exitError
	}

	kernel := &ear.Kernel{Workers: *workers}

	// Register every stack as an instance, then arm what each one authored.
	var armed int
	for _, dir := range positionals {
		name := instanceName(dir)
		runtime, err := loadStack(dir, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "ear kernel:", err)
			return exitError
		}
		defer runtime.Close()

		kernel.Register(name, runtime)
		// A one-shot run wants the authored work due now, not one period out.
		var armOpts []ear.TaskOption
		if *once {
			armOpts = append(armOpts, ear.StartAfter(0))
		}
		tasks, err := kernel.Arm(name, armOpts...)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ear kernel:", err)
			return exitError
		}
		armed += len(tasks)

		if !*asJSON {
			fmt.Printf("registered %-24s %s\n", name, describeSchedule(tasks))
		}
	}

	if armed == 0 {
		// An idle loop with nothing armed would sleep forever having been
		// asked to do nothing -- almost certainly a mis-authored memory.md
		// rather than an intention. A one-shot run with nothing armed is the
		// same mistake, caught the same way.
		fmt.Fprintln(os.Stderr, "ear kernel: no stack declares any `## Scheduled Work`; there is nothing to run")
		fmt.Fprintln(os.Stderr, "  author a schedule in memory.md, or use `ear run` for one-shot work")
		return exitError
	}

	// A claim, when given, applies to every scheduled task: the kernel is
	// acting on behalf of one identity for this whole session.
	if claim != nil {
		kernel.Dispatcher = claimedDispatcher(*claim)
	}

	if *once {
		dispatches := kernel.Drain(context.Background(), 0)
		for _, d := range dispatches {
			report(d, *asJSON)
		}
		return worstExit(dispatches)
	}

	return runKernelLoop(kernel, *asJSON, *statusEvery)
}

// runKernelLoop drives the idle loop until SIGINT/SIGTERM, reporting each
// dispatch as it lands, then drains cleanly.
func runKernelLoop(kernel *ear.Kernel, asJSON bool, statusEvery time.Duration) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !asJSON {
		fmt.Printf("\nkernel running -- %d instance(s), %d task(s) queued. Ctrl-C to stop.\n\n",
			len(kernel.Instances()), kernel.Pending())
	}

	// Report dispatches as they land by watching the history grow. The kernel
	// owns its own loop; this is a reader, so it never perturbs scheduling.
	done := make(chan struct{})
	go func() {
		defer close(done)
		reported := 0
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		var lastStatus time.Time
		for {
			select {
			case <-ctx.Done():
				for _, d := range kernel.History()[min(reported, len(kernel.History())):] {
					report(d, asJSON)
				}
				return
			case <-ticker.C:
				history := kernel.History()
				if reported > len(history) {
					reported = len(history) // history was trimmed under us
				}
				for _, d := range history[reported:] {
					report(d, asJSON)
				}
				reported = len(history)

				if statusEvery > 0 && time.Since(lastStatus) >= statusEvery {
					printSnapshot(kernel.Snapshot(), asJSON)
					lastStatus = time.Now()
				}
			}
		}
	}()

	err := kernel.Run(ctx)
	<-done

	if !asJSON {
		snap := kernel.Snapshot()
		fmt.Printf("\nkernel stopped -- %d dispatched, %d still queued\n", snap.Dispatched, snap.Pending)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "ear kernel:", err)
		return exitError
	}
	return exitDecided
}

// claimedDispatcher wraps in-process execution so every scheduled task runs
// under the operator's identity, and the instance's own tenant boundary
// decides whether it may.
func claimedDispatcher(claim ear.Claim) func(context.Context, *ear.Task, *ear.Runtime) (ear.DispatchStatus, string) {
	return func(ctx context.Context, task *ear.Task, rt *ear.Runtime) (ear.DispatchStatus, string) {
		decision, err := rt.Reason(ear.WithClaim(ctx, claim), task.Intent, task.Approval)
		if err != nil {
			return classify(err)
		}
		return ear.StatusRan, fmt.Sprint(decision)
	}
}

// classify mirrors the kernel's own governance/fault split for the wrapped
// dispatcher: a refusal EAR is designed to produce is blocked, not failed.
func classify(err error) (ear.DispatchStatus, string) {
	var (
		policy   *ear.PolicyViolationError
		approval *ear.ApprovalRequiredError
		tenant   *ear.TenantBoundaryError
	)
	switch {
	case errors.As(err, &approval), errors.As(err, &policy), errors.As(err, &tenant):
		return ear.StatusBlocked, err.Error()
	default:
		return ear.StatusFailed, err.Error()
	}
}

// parseClaim builds the operator's claim from the flags, insisting on both
// halves: a subject with no orgs would be refused everywhere, and orgs with no
// subject name nobody.
func parseClaim(subject, orgs string) (*ear.Claim, error) {
	subject, orgs = strings.TrimSpace(subject), strings.TrimSpace(orgs)
	if subject == "" && orgs == "" {
		return nil, nil
	}
	if subject == "" || orgs == "" {
		return nil, errors.New("-subject and -org must be given together")
	}
	var ids []string
	for _, id := range strings.Split(orgs, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("-org names no org ids")
	}
	return &ear.Claim{Subject: subject, OrgIDs: ids}, nil
}

// instanceName is the process-table name for a stack directory: its base name,
// which is what an operator would call it.
func instanceName(dir string) string {
	name := filepath.Base(filepath.Clean(dir))
	if name == "." || name == string(filepath.Separator) {
		abs, err := filepath.Abs(dir)
		if err == nil {
			name = filepath.Base(abs)
		}
	}
	return name
}

// describeSchedule summarizes what an instance was armed with.
func describeSchedule(tasks []*ear.Task) string {
	if len(tasks) == 0 {
		return "(no scheduled work authored)"
	}
	parts := make([]string, 0, len(tasks))
	for _, task := range tasks {
		parts = append(parts, fmt.Sprintf("every %s", task.Every))
	}
	return fmt.Sprintf("%d task(s): %s", len(tasks), strings.Join(parts, ", "))
}

// report prints one dispatch.
func report(d ear.Dispatch, asJSON bool) {
	if asJSON {
		payload, err := json.Marshal(map[string]any{
			"task_id": d.TaskID, "instance": d.Instance, "status": string(d.Status),
			"summary": d.Summary, "duration_ms": d.Duration.Milliseconds(),
			"at": d.At.Format(time.RFC3339),
		})
		if err == nil {
			fmt.Println(string(payload))
		}
		return
	}
	fmt.Printf("%s %-8s %-20s %s\n",
		d.At.Format("15:04:05"), d.Status, d.Instance, firstLine(d.Summary))
}

func printSnapshot(snap ear.Snapshot, asJSON bool) {
	if asJSON {
		payload, err := json.Marshal(map[string]any{
			"snapshot": true, "instances": snap.Instances, "pending": snap.Pending,
			"in_flight": snap.InFlight, "dispatched": snap.Dispatched, "idle_waits": snap.IdleWaits,
		})
		if err == nil {
			fmt.Println(string(payload))
		}
		return
	}
	fmt.Printf("  -- %d instance(s), %d pending, %d dispatched, %d idle wait(s)\n",
		len(snap.Instances), snap.Pending, snap.Dispatched, snap.IdleWaits)
}

// worstExit maps a batch of dispatches onto the CLI's governed exit codes, so
// `ear kernel -once` can be branched on the same way `ear run` is.
func worstExit(dispatches []ear.Dispatch) int {
	code := exitDecided
	for _, d := range dispatches {
		switch d.Status {
		case ear.StatusFailed:
			return exitError
		case ear.StatusBlocked:
			code = exitBlocked
		}
	}
	return code
}
