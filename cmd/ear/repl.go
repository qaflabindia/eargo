package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	ear "github.com/qaflabindia/ear"
)

// cmdRepl runs an interactive session against a stack. Each line is an
// intent (with optional trailing `-- key=value ...` context facts); a parked
// approval gate is answered with :approve / :reject, which re-run the parked
// intent with the verdict. The session's memory persists across restarts
// when the stack declares Cross-Session Data, and the trail persists when it
// declares a Reasoning Audit Trail -- both exactly as in `ear run`.
func cmdRepl(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: ear repl <stack-dir>")
		return exitError
	}
	runtime, err := loadStack(args[0], "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear repl:", err)
		return exitError
	}
	defer runtime.Close()

	fmt.Printf("%s -- %d processes loaded. An intent per line; :help for commands.\n",
		runtime.Name, len(runtime.Processes))
	if runtime.Memory.Len() > 0 {
		fmt.Printf("resumed session: %d remembered items\n", runtime.Memory.Len())
	}

	session := &replSession{runtime: runtime}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Print("ear> ")
		if !scanner.Scan() {
			fmt.Println()
			return exitDecided
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			if quit := session.command(line); quit {
				return exitDecided
			}
			continue
		}
		session.reason(line, nil)
	}
}

// replSession keeps the state a conversation needs: the runtime, and the
// last intent so an approval verdict can re-run it.
type replSession struct {
	runtime     *ear.Runtime
	lastIntent  *ear.Intent
	parked      bool
	parkedNames []string
}

// reason parses "intent text -- k=v k=v", runs the cycle, prints the outcome.
func (s *replSession) reason(line string, approval *ear.ApprovalVerdict) {
	text, factsPart, _ := strings.Cut(line, " -- ")
	facts := map[string]any{}
	if strings.TrimSpace(factsPart) != "" {
		parsed, err := parseContextPairs(strings.Fields(factsPart))
		if err != nil {
			fmt.Println("context error:", err)
			return
		}
		facts = parsed
	}
	intent := ear.NewIntent(strings.TrimSpace(text), facts)
	s.lastIntent = &intent
	s.run(intent, approval)
}

func (s *replSession) run(intent ear.Intent, approval *ear.ApprovalVerdict) {
	decision, err := s.runtime.Reason(context.Background(), intent, approval)
	outcome := classifyOutcome(s.runtime, decision, err)
	outcome.print(os.Stdout)
	s.parked = outcome.Status == "approval_required"
	s.parkedNames = outcome.PendingPolicies
}

// command handles one `:command`, returning true to quit.
func (s *replSession) command(line string) bool {
	fields := strings.Fields(line)
	command, rest := fields[0], fields[1:]
	switch command {
	case ":quit", ":q", ":exit":
		return true
	case ":help", ":h":
		fmt.Print(`an intent per line; add facts after " -- ":  Underwrite a loan -- loan_amount=20000 debt_to_income=0.3
  :approve [approver]   approve the parked gate and re-run the intent
  :reject [approver]    reject it instead
  :trail                the reasoning trail so far, rendered
  :usage                the usage ledger so far
  :verify               prove the in-memory trail's hash chain unbroken
  :memory               the remembered context window
  :stack                the loaded processes and policies
  :quit                 leave (memory persists if the stack declares it)
`)
	case ":approve", ":reject":
		if s.lastIntent == nil || !s.parked {
			fmt.Println("nothing is parked for approval")
			return false
		}
		verdict := command == ":approve"
		approval := &ear.ApprovalVerdict{Verdict: &verdict}
		if len(rest) > 0 {
			approval.Approver = rest[0]
		}
		if len(rest) > 1 {
			approval.Note = strings.Join(rest[1:], " ")
		}
		fmt.Printf("re-running %q with the verdict\n", s.lastIntent.Text)
		s.run(*s.lastIntent, approval)
	case ":trail":
		if trail := s.runtime.ReasoningLog.Render(); trail != "" {
			fmt.Print(trail)
		} else {
			fmt.Println("no reasoning recorded yet")
		}
	case ":usage":
		fmt.Print(s.runtime.ReasoningLog.UsageReport())
	case ":verify":
		ok, detail := s.runtime.ReasoningLog.Verify()
		if ok {
			fmt.Println("intact:", detail)
		} else {
			fmt.Println("BROKEN:", detail)
		}
	case ":memory":
		if window := s.runtime.Memory.ContextWindow(); window != "" {
			fmt.Println(window)
		} else {
			fmt.Println("nothing remembered yet")
		}
	case ":stack":
		fmt.Print(renderStack(s.runtime))
	default:
		fmt.Printf("unknown command %s (:help lists them)\n", command)
	}
	return false
}
