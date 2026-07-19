package ear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func toolRuntime(t *testing.T, name, desc string, h func(map[string]any) (string, error), params ...string) *Runtime {
	t.Helper()
	rt := NewRuntime("R")
	rt.Tools = []Tool{{Name: name, Description: desc}}
	if err := rt.BindTool(name, h, params...); err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestToolUseLoopCallsThenDecides(t *testing.T) {
	rt := toolRuntime(t, "echo", "echoes its input",
		func(args map[string]any) (string, error) { return fmt.Sprintf("echoed %v", args["msg"]), nil }, "msg")
	lm := &ScriptedLM{Replies: []string{
		Reply("tool", "echo", "arguments", "- msg: hi", "decision", ""), // call the tool
		Reply("tool", "", "decision", "APPROVED after echo"),            // then decide
	}}
	decision, err := reasonWithTools(context.Background(), rt, lm, "do it", "none", map[string]any{}, rt.ToolBinder.Tools())
	if err != nil {
		t.Fatal(err)
	}
	if decision != "APPROVED after echo" {
		t.Errorf("decision = %q", decision)
	}
	var sawEcho bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "tool" && strings.Contains(rec.Output, "echoed hi") {
			sawEcho = true
		}
	}
	if !sawEcho {
		t.Error("expected the echo tool call recorded on the trail")
	}
}

func TestToolLoopRefusesRepeatedFailedCall(t *testing.T) {
	calls := 0
	rt := toolRuntime(t, "flaky", "always fails",
		func(map[string]any) (string, error) { calls++; return "", errors.New("boom") }, "x")
	lm := &ScriptedLM{Replies: []string{
		Reply("tool", "flaky", "arguments", "- x: 1", "decision", ""), // runs, fails
		Reply("tool", "flaky", "arguments", "- x: 1", "decision", ""), // identical -> refused, not run
		Reply("tool", "", "decision", "GAVE UP"),
	}}
	decision, err := reasonWithTools(context.Background(), rt, lm, "do it", "none", map[string]any{}, rt.ToolBinder.Tools())
	if err != nil {
		t.Fatal(err)
	}
	if decision != "GAVE UP" {
		t.Errorf("decision = %q", decision)
	}
	if calls != 1 {
		t.Errorf("an unchanged failed call must be refused, not re-run; handler ran %d times", calls)
	}
}

func TestToolLoopRecoversFromUnknownTool(t *testing.T) {
	rt := toolRuntime(t, "real", "a real tool",
		func(map[string]any) (string, error) { return "ok", nil })
	lm := &ScriptedLM{Replies: []string{
		Reply("tool", "ghost", "decision", ""),   // nonexistent tool, no decision
		Reply("tool", "", "decision", "DECIDED"), // recovered -> decide
	}}
	decision, err := reasonWithTools(context.Background(), rt, lm, "do it", "none", map[string]any{}, rt.ToolBinder.Tools())
	if err != nil {
		t.Fatal(err)
	}
	if decision != "DECIDED" {
		t.Errorf("decision = %q", decision)
	}
}

func TestToolLoopBudgetSpentConcludes(t *testing.T) {
	rt := toolRuntime(t, "echo", "echoes",
		func(args map[string]any) (string, error) { return "echoed", nil }, "msg")
	rt.ToolBinder.MaxIterations = 2
	// The model keeps calling the (succeeding, but varied) tool and never
	// decides; after the budget the loop concludes via ReasonAboutIntent.
	lm := &ScriptedLM{
		Replies: []string{
			Reply("tool", "echo", "arguments", "- msg: a", "decision", ""),
			Reply("tool", "echo", "arguments", "- msg: b", "decision", ""),
		},
		Default: Reply("decision", "CONCLUDED from gathered facts"),
	}
	decision, err := reasonWithTools(context.Background(), rt, lm, "do it", "none", map[string]any{}, rt.ToolBinder.Tools())
	if err != nil {
		t.Fatal(err)
	}
	if decision != "CONCLUDED from gathered facts" {
		t.Errorf("decision after budget = %q", decision)
	}
}
