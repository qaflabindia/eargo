package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	ear "github.com/qaflabindia/ear"
)

// cmdRun reasons one intent through a stack and reports the governed outcome:
// the decision with its explanation, a policy block with the policies named,
// or a parked approval gate -- each with its own exit code so scripts can
// branch without parsing prose.
func cmdRun(args []string) int {
	flags := flag.NewFlagSet("ear run", flag.ContinueOnError)
	var facts contextFlags
	flags.Var(&facts, "c", "context fact key=value (repeatable)")
	approve := flags.Bool("approve", false, "submit a human approval for the parked gate")
	reject := flags.Bool("reject", false, "submit a human rejection instead")
	approver := flags.String("approver", "", "who is giving the verdict")
	note := flags.String("note", "", "a note attached to the verdict")
	name := flags.String("name", "", "override the runtime name")
	asJSON := flags.Bool("json", false, "machine output: one JSON object on stdout")
	flagArgs, positionals := reorderArgs(args, map[string]bool{
		"approver": true, "note": true, "name": true, "c": true,
	})
	if err := flags.Parse(flagArgs); err != nil {
		return exitError
	}
	if len(positionals) < 2 {
		fmt.Fprintln(os.Stderr, `usage: ear run <stack-dir> "<intent>" [flags]`)
		return exitError
	}
	if *approve && *reject {
		fmt.Fprintln(os.Stderr, "ear run: -approve and -reject are mutually exclusive")
		return exitError
	}

	runtime, err := loadStack(positionals[0], *name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear run:", err)
		return exitError
	}
	defer runtime.Close()

	intentContext, err := parseContextPairs(positionals[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear run:", err)
		return exitError
	}
	for _, pair := range facts {
		extra, err := parseContextPairs([]string{pair})
		if err != nil {
			fmt.Fprintln(os.Stderr, "ear run:", err)
			return exitError
		}
		for k, v := range extra {
			intentContext[k] = v
		}
	}

	var approval *ear.ApprovalVerdict
	if *approve || *reject {
		verdict := *approve
		approval = &ear.ApprovalVerdict{Approver: *approver, Verdict: &verdict, Note: *note}
	}

	intent := ear.NewIntent(positionals[1], intentContext)
	decision, err := runtime.Reason(context.Background(), intent, approval)
	outcome := classifyOutcome(runtime, decision, err)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(outcome)
	} else {
		outcome.print(os.Stdout)
	}
	return outcome.exitCode()
}

// runOutcome is one cycle's result in a fixed shape, printed as prose or JSON.
type runOutcome struct {
	Status          string   `json:"status"` // decided | blocked | approval_required | error
	Decision        string   `json:"decision,omitempty"`
	Explanation     string   `json:"explanation,omitempty"`
	BlockedPolicies []string `json:"blocked_policies,omitempty"`
	PendingPolicies []string `json:"pending_policies,omitempty"`
	Error           string   `json:"error,omitempty"`
	Usage           string   `json:"usage,omitempty"`
}

// classifyOutcome maps a Reason result onto the fixed outcome shape, pulling
// the explanation and usage line off the cycle's trail.
func classifyOutcome(runtime *ear.Runtime, decision any, err error) runOutcome {
	outcome := runOutcome{}
	if cycle := runtime.ReasoningLog.LastCycle(); cycle != nil {
		for _, record := range cycle.Records {
			switch record.Stage {
			case "explanation":
				outcome.Explanation = record.Output
			case "usage":
				outcome.Usage = record.Output
			}
		}
	}
	switch {
	case err == nil:
		outcome.Status = "decided"
		outcome.Decision = fmt.Sprint(decision)
	default:
		var violation *ear.PolicyViolationError
		var pending *ear.ApprovalRequiredError
		switch {
		case errors.As(err, &violation):
			outcome.Status = "blocked"
			for _, p := range violation.Policies {
				outcome.BlockedPolicies = append(outcome.BlockedPolicies, p.Name)
			}
		case errors.As(err, &pending):
			outcome.Status = "approval_required"
			for _, p := range pending.Policies {
				outcome.PendingPolicies = append(outcome.PendingPolicies, p.Name)
			}
		default:
			outcome.Status = "error"
		}
		outcome.Error = err.Error()
	}
	return outcome
}

func (o runOutcome) exitCode() int {
	switch o.Status {
	case "decided":
		return exitDecided
	case "blocked":
		return exitBlocked
	case "approval_required":
		return exitApproval
	}
	return exitError
}

func (o runOutcome) print(w *os.File) {
	switch o.Status {
	case "decided":
		fmt.Fprintln(w, "decision:", o.Decision)
		if o.Explanation != "" {
			fmt.Fprintln(w, "why:     ", o.Explanation)
		}
	case "blocked":
		fmt.Fprintln(w, "blocked by policy:", joinList(o.BlockedPolicies))
	case "approval_required":
		fmt.Fprintln(w, "parked for human approval:", joinList(o.PendingPolicies))
		fmt.Fprintln(w, "re-run with -approve or -reject (and -approver) to give the verdict")
	default:
		fmt.Fprintln(w, "error:", o.Error)
	}
	if o.Usage != "" {
		fmt.Fprintln(w, "usage:   ", o.Usage)
	}
}

func joinList(names []string) string {
	if len(names) == 0 {
		return "(none named)"
	}
	out := names[0]
	for _, n := range names[1:] {
		out += ", " + n
	}
	return out
}
