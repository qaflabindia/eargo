package ear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestBindToolRequiresDeclaration(t *testing.T) {
	rt := NewRuntime("R")
	if err := rt.BindTool("undeclared", func(map[string]any) (string, error) { return "x", nil }); err == nil {
		t.Fatal("binding an undeclared tool should fail loudly")
	}
	rt.Tools = []Tool{{Name: "calc", Description: "adds numbers"}}
	if err := rt.BindTool("calc", func(map[string]any) (string, error) { return "ok", nil }, "a", "b"); err != nil {
		t.Fatalf("declared tool should bind: %v", err)
	}
	bt, ok := rt.ToolBinder.Get("Calc") // name lookup is normalized
	if !ok || bt.Description != "adds numbers" || len(bt.Parameters) != 2 {
		t.Errorf("bound tool = %+v", bt)
	}
}

func TestInvokeToolRecordsAndReturns(t *testing.T) {
	rt := NewRuntime("R")
	rt.Tools = []Tool{{Name: "echo", Description: "echoes"}}
	_ = rt.BindTool("echo", func(args map[string]any) (string, error) {
		return fmt.Sprintf("echoed %v", args["msg"]), nil
	}, "msg")
	out := rt.InvokeTool(context.Background(), "echo", map[string]any{"msg": "hi"})
	if out != "echoed hi" {
		t.Errorf("out = %q", out)
	}
	var saw bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "tool" && strings.Contains(rec.Output, "echoed hi") {
			saw = true
		}
	}
	if !saw {
		t.Error("expected a tool record carrying the output")
	}
}

func TestInvokeToolPolicyBlocksThisCall(t *testing.T) {
	rt := NewRuntime("R")
	rt.Tools = []Tool{{Name: "wire_transfer", Description: "send money"}}
	called := false
	_ = rt.BindTool("wire_transfer", func(map[string]any) (string, error) { called = true; return "sent", nil }, "amount")
	rt.ToolPolicies = []*Policy{{Name: "Transfer Cap", FallbackExpression: "amount <= 10000"}}

	out := rt.InvokeTool(context.Background(), "wire_transfer", map[string]any{"amount": 50000.0})
	if called {
		t.Error("a blocked call must not run the handler")
	}
	if !strings.Contains(out, "blocked by policy 'Transfer Cap'") {
		t.Errorf("refusal = %q", out)
	}
	var blocked bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "tool" && strings.Contains(rec.Output, "BLOCKED") {
			blocked = true
		}
	}
	if !blocked {
		t.Error("expected a BLOCKED tool record")
	}

	// A compliant amount runs -- the policy bit the call, not the tool.
	if out := rt.InvokeTool(context.Background(), "wire_transfer", map[string]any{"amount": 5000.0}); !called || out != "sent" {
		t.Errorf("compliant call: called=%v out=%q", called, out)
	}
}

func TestInvokeToolHandlerErrorIsText(t *testing.T) {
	rt := NewRuntime("R")
	rt.Tools = []Tool{{Name: "flaky", Description: "fails"}}
	_ = rt.BindTool("flaky", func(map[string]any) (string, error) { return "", errors.New("boom") })
	out := rt.InvokeTool(context.Background(), "flaky", nil)
	if !strings.Contains(out, "failed: boom") {
		t.Errorf("a handler error should return as text, got %q", out)
	}
	var failed bool
	for rec := range rt.ReasoningLog.Records() {
		if rec.Stage == "tool" && strings.Contains(rec.Output, "FAILED") {
			failed = true
		}
	}
	if !failed {
		t.Error("expected a FAILED tool record")
	}
}

func TestInvokeUnknownTool(t *testing.T) {
	rt := NewRuntime("R")
	if out := rt.InvokeTool(context.Background(), "ghost", nil); !strings.Contains(out, "no tool named") {
		t.Errorf("out = %q", out)
	}
}
