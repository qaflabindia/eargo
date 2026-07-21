package ear

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the MCP stub server. With EAR_MCP_STUB set, this binary
// acts as a real MCP server over stdio and exits without running any tests --
// so the client is exercised against an actual subprocess speaking actual
// JSON-RPC, not a mocked transport. The behaviour under test is chosen by
// EAR_MCP_STUB_MODE.
//
// Doing it here rather than in a `-test.run`-selected test keeps the stub from
// appearing as a permanently skipped test: a skip is how coverage disappears
// unnoticed, and the suite asserts there are none.
func TestMain(m *testing.M) {
	if os.Getenv("EAR_MCP_STUB") != "" {
		runStubServer(os.Getenv("EAR_MCP_STUB_MODE"))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runStubServer(mode string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	reply := func(id any, result map[string]any) {
		encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		fmt.Fprintln(out, string(encoded))
		out.Flush()
	}

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var request map[string]any
		if json.Unmarshal([]byte(line), &request) != nil {
			continue
		}
		method, _ := request["method"].(string)
		id := request["id"]

		if mode == "garbage" {
			// A server that also logs to stdout: the client must skip the
			// noise rather than tear the connection down.
			fmt.Fprintln(out, "starting up, please wait")
			fmt.Fprintln(out, "{ not json at all")
			out.Flush()
		}

		switch method {
		case "initialize":
			reply(id, map[string]any{
				"protocolVersion": MCPProtocolVersion,
				"serverInfo":      map[string]any{"name": "stub", "version": "1"},
			})
		case "notifications/initialized":
			// A notification: no reply, by definition.
		case "tools/list":
			reply(id, map[string]any{"tools": []map[string]any{
				{
					"name":        "echo",
					"description": "echoes its message back",
					"inputSchema": map[string]any{"properties": map[string]any{
						"message": map[string]any{"type": "string"},
					}},
				},
				{
					"name":        "explode",
					"description": "always reports an error",
					"inputSchema": map[string]any{"properties": map[string]any{}},
				},
			}})
		case "tools/call":
			params, _ := request["params"].(map[string]any)
			name, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]any)

			if mode == "hang" {
				continue // never answer: the client's timeout must save it
			}
			if name == "explode" {
				reply(id, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "the tool failed"}},
					"isError": true,
				})
				continue
			}
			message, _ := args["message"].(string)
			reply(id, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "echo: " + message}},
			})
		default:
			encoded, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32601, "message": "no such method: " + method},
			})
			fmt.Fprintln(out, string(encoded))
			out.Flush()
		}
	}
}

// stubServer returns a declaration whose launch command re-executes this test
// binary as the stub MCP server.
func stubServer(t *testing.T, mode string) McpServer {
	t.Helper()
	t.Setenv("EAR_MCP_STUB", "1")
	t.Setenv("EAR_MCP_STUB_MODE", mode)
	return McpServer{
		Name:        "stub",
		Description: "a test MCP server",
		Command:     fmt.Sprintf("%q", os.Args[0]),
	}
}

// -- the declaration ---------------------------------------------------------

func TestStrategyParsesMcpDeclarations(t *testing.T) {
	s := StrategyFromMarkdown("# Memory\n\n## MCP\n\n" +
		"- credit_bureau: pulls credit reports, via `bureau-mcp-server --stdio`\n" +
		"- core_banking: reads balances, at https://banking.internal/mcp\n")

	if len(s.McpServers) != 2 {
		t.Fatalf("want 2 declared servers, got %d: %+v", len(s.McpServers), s.McpServers)
	}
	if s.McpServers[0].Command != "bureau-mcp-server --stdio" {
		t.Errorf("command = %q", s.McpServers[0].Command)
	}
	if !strings.Contains(s.McpServers[0].Description, "credit reports") {
		t.Errorf("description = %q", s.McpServers[0].Description)
	}
	if s.McpServers[1].URL != "https://banking.internal/mcp" {
		t.Errorf("url = %q", s.McpServers[1].URL)
	}

	found, ok := s.McpServerNamed("Credit Bureau")
	if !ok || found.Name != "credit_bureau" {
		t.Errorf("lookup should be case- and punctuation-insensitive, got %+v %v", found, ok)
	}
	if _, ok := s.McpServerNamed("nope"); ok {
		t.Error("an undeclared server must not resolve")
	}
}

func TestMcpServerDescribe(t *testing.T) {
	s := McpServer{Name: "bureau", Description: "pulls reports", Command: "bureau-mcp"}
	if got := s.Describe(); got != "bureau: pulls reports (reached via `bureau-mcp`)" {
		t.Errorf("describe = %q", got)
	}
}

func TestCommandWords(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"server", []string{"server"}},
		{"server --stdio -v", []string{"server", "--stdio", "-v"}},
		{`"/path with spaces/server" --stdio`, []string{"/path with spaces/server", "--stdio"}},
		{"  spaced   out  ", []string{"spaced", "out"}},
		{"", nil},
	} {
		got := commandWords(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%q -> %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q -> %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

// -- the transport, against a real subprocess --------------------------------

func TestMcpClientHandshakeListAndCall(t *testing.T) {
	client := NewMcpClient(stubServer(t, "normal"))
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d: %+v", len(tools), tools)
	}
	if tools[0].Name != "echo" || tools[0].Parameters[0] != "message" {
		t.Errorf("tool 0 = %+v", tools[0])
	}

	result, err := client.CallTool(ctx, "echo", map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "echo: hello" {
		t.Errorf("result = %q", result)
	}
}

func TestMcpClientSurfacesAToolReportedError(t *testing.T) {
	client := NewMcpClient(stubServer(t, "normal"))
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	_, err := client.CallTool(ctx, "explode", nil)
	var mcpErr *McpError
	if !errors.As(err, &mcpErr) {
		t.Fatalf("want *McpError, got %T (%v)", err, err)
	}
	if !strings.Contains(err.Error(), "the tool failed") {
		t.Errorf("the error should carry the server's message, got %q", err.Error())
	}
}

func TestMcpClientTimesOutRatherThanHanging(t *testing.T) {
	client := NewMcpClient(stubServer(t, "hang"))
	client.Timeout = 250 * time.Millisecond
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	started := time.Now()
	_, err := client.CallTool(ctx, "echo", map[string]any{"message": "hi"})
	if err == nil {
		t.Fatal("a server that never answers must not hang the runtime")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("want a timeout, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the timeout did not bound the wait: %v", elapsed)
	}
}

func TestMcpClientHonoursContextCancellation(t *testing.T) {
	client := NewMcpClient(stubServer(t, "hang"))
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()

	_, err := client.CallTool(ctx, "echo", map[string]any{"message": "hi"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestMcpClientSkipsNonJSONNoise(t *testing.T) {
	// A server that logs to stdout is common and not fatal; the client should
	// step over the noise rather than tearing the connection down.
	client := NewMcpClient(stubServer(t, "garbage"))
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect should survive noisy output: %v", err)
	}
	defer client.Close()

	result, err := client.CallTool(ctx, "echo", map[string]any{"message": "still here"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "echo: still here" {
		t.Errorf("result = %q", result)
	}
}

func TestMcpClientRefusesUnreachableDeclarations(t *testing.T) {
	// A URL-only declaration is a different transport; half-attempting it
	// would fail later and less clearly.
	client := NewMcpClient(McpServer{Name: "remote", URL: "https://example.internal/mcp"})
	err := client.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stdio") {
		t.Errorf("want a clear stdio-only refusal, got %v", err)
	}

	client = NewMcpClient(McpServer{Name: "bare"})
	if err := client.Connect(context.Background()); err == nil {
		t.Error("a server with no way to reach it must be refused")
	}
}

func TestMcpClientReportsALaunchFailure(t *testing.T) {
	client := NewMcpClient(McpServer{Name: "missing", Command: "definitely-not-a-real-binary-xyz"})
	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("launching a nonexistent binary must fail loudly")
	}
	var mcpErr *McpError
	if !errors.As(err, &mcpErr) {
		t.Errorf("want *McpError, got %T", err)
	}
}

func TestMcpClientCloseIsIdempotent(t *testing.T) {
	client := NewMcpClient(stubServer(t, "normal"))
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	// And a call after closing is an error, not a hang.
	if _, err := client.CallTool(context.Background(), "echo", nil); err == nil {
		t.Error("calling a closed client should fail")
	}
}

// -- wiring into the runtime -------------------------------------------------

func mcpRuntime(t *testing.T, mode string) *Runtime {
	t.Helper()
	server := stubServer(t, mode)
	r := NewRuntime("MCP Runtime")
	r.Strategy = &Strategy{McpServers: []McpServer{server}}
	workflow := (&Workflow{Name: "W"}).AddPersona(&Persona{Name: "Analyst", Instructions: "Analyse."})
	r.AddProcess((&Process{Name: "P", Description: "Work."}).AddWorkflow(workflow))
	return r
}

func TestConnectMCPBindsTheServersTools(t *testing.T) {
	r := mcpRuntime(t, "normal")
	ctx := context.Background()

	client, err := r.ConnectMCP(ctx, "stub")
	if err != nil {
		t.Fatalf("ConnectMCP: %v", err)
	}
	defer client.Close()

	// Namespaced by server, so two servers offering the same tool name stay
	// distinguishable.
	tool, ok := r.ToolBinder.Get("stub.echo")
	if !ok {
		var names []string
		for _, bound := range r.ToolBinder.Tools() {
			names = append(names, bound.Name)
		}
		t.Fatalf("stub.echo not bound; bound: %v", names)
	}
	if tool.Description != "echoes its message back" {
		t.Errorf("description = %q", tool.Description)
	}

	// And it runs through the governed invocation path.
	result := r.InvokeTool(ctx, "stub.echo", map[string]any{"message": "via the binder"})
	if result != "echo: via the binder" {
		t.Errorf("result = %q", result)
	}
}

func TestConnectMCPRefusesAnUndeclaredServer(t *testing.T) {
	// The declaration is the authorization: code must not grow the runtime a
	// capability the natural-language authoring does not show.
	r := mcpRuntime(t, "normal")
	_, err := r.ConnectMCP(context.Background(), "somewhere-else")
	if err == nil {
		t.Fatal("an undeclared server must be refused")
	}
	if !strings.Contains(err.Error(), "not declared") {
		t.Errorf("the refusal should say why, got %v", err)
	}

	bare := NewRuntime("No Strategy")
	if _, err := bare.ConnectMCP(context.Background(), "stub"); err == nil {
		t.Error("a runtime with no strategy declares no servers")
	}
}

func TestMcpToolCallIsRecordedOnTheTrail(t *testing.T) {
	r := mcpRuntime(t, "normal")
	ctx := context.Background()
	client, err := r.ConnectMCP(ctx, "stub")
	if err != nil {
		t.Fatalf("ConnectMCP: %v", err)
	}
	defer client.Close()

	r.InvokeTool(ctx, "stub.echo", map[string]any{"message": "audited"})

	var connected, called bool
	for record := range r.ReasoningLog.Records() {
		if record.Stage == "mcp" && strings.Contains(record.Output, "bound 2 tool(s)") {
			connected = true
		}
		if record.Stage == "tool" && strings.Contains(record.Output, "echo: audited") {
			called = true
		}
	}
	if !connected {
		t.Error("connecting to a server should be on the trail")
	}
	if !called {
		t.Error("an MCP tool call should be a tool record like any other")
	}
}

func TestMcpToolIsJudgedByToolPolicies(t *testing.T) {
	// An MCP tool is an ordinary bound tool, so a tool-scoped policy governs
	// it exactly as it governs a native one.
	r := mcpRuntime(t, "normal")
	r.ToolPolicies = append(r.ToolPolicies, &Policy{
		Name:               "No Shouting",
		Statement:          "The echo tool must not be given an urgent message.",
		FallbackExpression: "urgent == false",
	})
	ctx := context.Background()
	client, err := r.ConnectMCP(ctx, "stub")
	if err != nil {
		t.Fatalf("ConnectMCP: %v", err)
	}
	defer client.Close()

	blocked := r.InvokeTool(ctx, "stub.echo", map[string]any{"message": "hi", "urgent": true})
	if !strings.Contains(blocked, "blocked by policy") {
		t.Errorf("a tool-scoped policy should block the MCP call, got %q", blocked)
	}

	allowed := r.InvokeTool(ctx, "stub.echo", map[string]any{"message": "hi", "urgent": false})
	if allowed != "echo: hi" {
		t.Errorf("a compliant call should go through, got %q", allowed)
	}
}

func TestMcpToolFailureReturnsAsTextNotACrash(t *testing.T) {
	r := mcpRuntime(t, "normal")
	ctx := context.Background()
	client, err := r.ConnectMCP(ctx, "stub")
	if err != nil {
		t.Fatalf("ConnectMCP: %v", err)
	}
	defer client.Close()

	result := r.InvokeTool(ctx, "stub.explode", nil)
	if !strings.Contains(result, "failed") {
		t.Errorf("a failing MCP tool should return to the model as text, got %q", result)
	}
}
