package main

import (
	"fmt"
	"os"
	"strings"

	ear "github.com/qaflabindia/ear"
)

// cmdTrail renders a persisted reasoning trail readable. A JSONL trail is
// reconstructed losslessly and rendered; a markdown trail already is the
// readable view, so it prints as-is.
func cmdTrail(args []string) int {
	path, code := trailArg("trail", args)
	if code != exitDecided {
		return code
	}
	if strings.HasSuffix(path, ".md") {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ear trail:", err)
			return exitError
		}
		os.Stdout.Write(data)
		return exitDecided
	}
	log, err := ear.ReadTrail(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear trail:", err)
		return exitError
	}
	if rendered := log.Render(); rendered != "" {
		fmt.Print(rendered)
	} else {
		fmt.Println("the trail is empty")
	}
	return exitDecided
}

// cmdUsage renders the usage ledger from a persisted JSONL trail.
func cmdUsage(args []string) int {
	path, code := trailArg("usage", args)
	if code != exitDecided {
		return code
	}
	log, err := ear.ReadTrail(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ear usage:", err)
		return exitError
	}
	fmt.Print(log.UsageReport())
	return exitDecided
}

// cmdVerify proves a persisted trail's hash chain unbroken -- either codec.
// Exit 0 when intact, 1 when the chain is broken, 2 on a usage error.
func cmdVerify(args []string) int {
	path, code := trailArg("verify", args)
	if code != exitDecided {
		return code
	}
	ok, detail := ear.VerifyTrail(path)
	if ok {
		fmt.Printf("intact: %s (%s)\n", detail, path)
		return exitDecided
	}
	fmt.Printf("BROKEN: %s (%s)\n", detail, path)
	return exitBlocked
}

// trailArg resolves the single trail argument for trail/usage/verify.
func trailArg(command string, args []string) (string, int) {
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: ear %s <stack-dir|trail-file>\n", command)
		return "", exitError
	}
	path, err := resolveTrailPath(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "ear %s: %v\n", command, err)
		return "", exitError
	}
	return path, exitDecided
}
