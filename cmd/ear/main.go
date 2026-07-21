// Command ear runs an EAR stack: a directory of plain-English markdown
// (skills.md, persona.md, workflow.md, process.md, policy.md, tenant.md,
// memory.md) assembled into a governed reasoning runtime.
//
// Usage:
//
//	ear run <stack-dir> "<intent>" [flags]   reason one intent through the stack
//	ear repl <stack-dir>                     interactive session (persists across runs)
//	ear inspect <stack-dir>                  show the assembled stack and strategy
//	ear trail <stack-dir|trail-file>         render the persisted reasoning trail
//	ear usage <stack-dir|trail-file>         the usage ledger from the persisted trail
//	ear verify <stack-dir|trail-file>        prove the trail's hash chain unbroken
//	ear demo                                 run the built-in demonstration stack
//
// Exit codes: 0 decided (or command succeeded), 1 blocked by policy (or a
// broken trail chain), 2 usage/load error, 3 parked for human approval.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ear "github.com/qaflabindia/ear"
)

// Exit codes, so scripts can branch on the governed outcomes.
const (
	exitDecided  = 0
	exitBlocked  = 1
	exitError    = 2
	exitApproval = 3
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return exitError
	}
	command, rest := args[0], args[1:]
	switch command {
	case "run":
		return cmdRun(rest)
	case "repl":
		return cmdRepl(rest)
	case "inspect":
		return cmdInspect(rest)
	case "trail":
		return cmdTrail(rest)
	case "usage":
		return cmdUsage(rest)
	case "verify":
		return cmdVerify(rest)
	case "demo":
		return cmdDemo(rest)
	case "help", "-h", "--help":
		usage(os.Stdout)
		return exitDecided
	default:
		fmt.Fprintf(os.Stderr, "ear: unknown command %q\n\n", command)
		usage(os.Stderr)
		return exitError
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `ear -- run an EAR stack: plain-English markdown assembled into a governed reasoning runtime

usage:
  ear run <stack-dir> "<intent>" [flags]   reason one intent through the stack
  ear repl <stack-dir>                     interactive session (persists across runs)
  ear inspect <stack-dir>                  show the assembled stack and strategy
  ear trail <stack-dir|trail-file>         render the persisted reasoning trail
  ear usage <stack-dir|trail-file>         the usage ledger from the persisted trail
  ear verify <stack-dir|trail-file>        prove the trail's hash chain unbroken
  ear demo                                 run the built-in demonstration stack
  ear help                                 this help

run flags:
  -c key=value      a context fact (repeatable); values coerce to numbers/booleans
  -approve          submit a human approval for the intent's parked gate
  -reject           submit a human rejection instead
  -approver name    who is giving the verdict (validated against the policy's approvers)
  -note text        a note attached to the verdict
  -name text        override the runtime name (default: the stack's title)
  -json             machine output: one JSON object on stdout

exit codes: 0 decided, 1 blocked by policy (or broken trail chain), 2 error, 3 parked for approval
`)
}

// loadStack loads a runtime from a stack directory, with a uniform error.
func loadStack(dir, name string) (*ear.Runtime, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%q is not a stack directory", dir)
	}
	runtime, err := ear.LoadRuntime(dir, name)
	if err != nil {
		return nil, fmt.Errorf("loading the stack at %q: %w", dir, err)
	}
	return runtime, nil
}

// parseContextPairs turns repeated key=value strings into an intent context,
// coercing values the same way the markdown loader coerces them.
func parseContextPairs(pairs []string) (map[string]any, error) {
	context := map[string]any{}
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("context fact %q is not key=value", pair)
		}
		context[strings.TrimSpace(key)] = ear.Coerce(value)
	}
	return context, nil
}

// resolveTrailPath resolves a trail argument: a file path is itself; a stack
// directory resolves to the trail its memory.md declares.
func resolveTrailPath(arg string) (string, error) {
	info, err := os.Stat(arg)
	if err != nil {
		return "", fmt.Errorf("no file or stack directory at %q", arg)
	}
	if !info.IsDir() {
		return arg, nil
	}
	data, err := os.ReadFile(filepath.Join(arg, "memory.md"))
	if err != nil {
		return "", fmt.Errorf("stack %q has no memory.md to declare a trail", arg)
	}
	strategy := ear.StrategyFromMarkdown(string(data))
	if !strategy.AuditEnabled || strategy.AuditPath == "" {
		return "", fmt.Errorf("stack %q declares no reasoning audit trail in memory.md", arg)
	}
	path := strategy.AuditPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(arg, path)
	}
	return path, nil
}

// contextFlags collects repeated -c key=value flags.
type contextFlags []string

func (c *contextFlags) String() string { return strings.Join(*c, ", ") }
func (c *contextFlags) Set(v string) error {
	*c = append(*c, v)
	return nil
}

// reorderArgs separates flags from positionals wherever they appear, so
// `ear run <dir> "<intent>" -approve` works -- the flag package alone stops
// parsing flags at the first positional. valueFlags names the flags that
// take a value in the following argument (the -flag=value form needs no
// help).
func reorderArgs(args []string, valueFlags map[string]bool) (flagArgs, positionals []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
			positionals = append(positionals, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		bare, _, hasValue := strings.Cut(arg, "=")
		if !hasValue && valueFlags[strings.TrimLeft(bare, "-")] && i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return flagArgs, positionals
}
