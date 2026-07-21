package ear

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// McpClient -- EAR's own MCP (Model Context Protocol) client, spoken from the
// Go standard library alone.
//
// MCP is an open JSON-RPC 2.0 protocol; EAR speaks it directly, no SDK. This
// launches a declared server as a subprocess and talks to it over stdio --
// line-delimited JSON-RPC on the child's stdin/stdout -- performing the
// handshake (initialize / notifications/initialized), listing its tools
// (tools/list) and calling them (tools/call). The transport is a couple of
// hundred lines because the protocol *is* the spec, and the spec is JSON over
// pipes.
//
// A connected server's tools become ordinary BoundTools (see
// Runtime.ConnectMCP), so every MCP call runs through InvokeTool: it is a
// `tool` trail record with its arguments, result and duration, obeys the same
// tool-loop budget, and is judged by the same tool-scoped policies as any
// native tool. A server that hangs or answers with malformed JSON fails as an
// *McpError, never silently -- and, wrapped by the binder, that failure
// returns to the model as text like any other tool failure.
//
// Exactly one goroutine ever reads the server's stdout: the pump that Connect
// starts, for the connection's whole lifetime. Each in-flight request
// registers a one-slot channel keyed by its JSON-RPC id, and the pump routes
// what it reads to the matching channel, dropping anything nobody is waiting
// for -- a response that outlived its caller's deadline, or a notification, is
// not an error. A single persistent reader is what makes a client-side timeout
// safe: nothing ever spawns a second reader that could race the pump for the
// same bytes and silently steal a line meant for a later call.
type McpClient struct {
	// Server is the declared server this client speaks to.
	Server McpServer

	// Timeout bounds a single request. Zero uses DefaultMcpTimeout.
	Timeout time.Duration

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	mu      sync.Mutex
	nextID  int
	pending map[int]chan *rpcMessage
	closed  bool
	pumpErr error

	pumpDone chan struct{}
}

// MCPProtocolVersion is the protocol revision this client negotiates.
const MCPProtocolVersion = "2024-11-05"

// DefaultMcpTimeout bounds a single request when none is configured.
const DefaultMcpTimeout = 30 * time.Second

// McpError is a failed MCP interaction: launch, handshake, transport, timeout,
// a JSON-RPC error, or a malformed response. Loud by design -- a server that
// misbehaves is a fact the runtime surfaces, never swallows.
type McpError struct {
	Server string
	Op     string
	Err    error
}

func (e *McpError) Error() string {
	return fmt.Sprintf("mcp server %q: %s: %v", e.Server, e.Op, e.Err)
}

func (e *McpError) Unwrap() error { return e.Err }

func mcpErrorf(server, op, format string, args ...any) *McpError {
	return &McpError{Server: server, Op: op, Err: fmt.Errorf(format, args...)}
}

// McpTool is one tool a connected server advertises: its name, the description
// the model reads, and the parameter names from its JSON schema.
type McpTool struct {
	Name        string
	Description string
	Parameters  []string
}

// rpcMessage is one JSON-RPC frame in either direction.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewMcpClient builds a client for a declared server. Connect starts it.
func NewMcpClient(server McpServer) *McpClient {
	return &McpClient{Server: server, pending: map[int]chan *rpcMessage{}}
}

func (c *McpClient) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultMcpTimeout
}

// Connect launches the server and performs the MCP handshake.
//
// Only a stdio server (one declared with a launch command) is supported. A
// declaration carrying only a URL is refused rather than half-attempted: HTTP
// transport is a different transport, and pretending otherwise would fail
// later and less clearly.
func (c *McpClient) Connect(ctx context.Context) error {
	if c.Server.Command == "" {
		if c.Server.URL != "" {
			return mcpErrorf(c.Server.Name, "connect",
				"declared with a URL (%s) but this client speaks stdio only; declare a launch command instead",
				c.Server.URL)
		}
		return mcpErrorf(c.Server.Name, "connect", "declares no launch command to reach it")
	}

	words := commandWords(c.Server.Command)
	if len(words) == 0 {
		return mcpErrorf(c.Server.Name, "connect", "launch command is empty")
	}

	cmd := exec.Command(words[0], words[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return &McpError{Server: c.Server.Name, Op: "connect", Err: err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &McpError{Server: c.Server.Name, Op: "connect", Err: err}
	}
	if err := cmd.Start(); err != nil {
		return &McpError{Server: c.Server.Name, Op: "launch", Err: err}
	}

	c.mu.Lock()
	c.cmd, c.stdin, c.stdout = cmd, stdin, stdout
	if c.pending == nil {
		c.pending = map[int]chan *rpcMessage{}
	}
	c.closed = false
	c.pumpDone = make(chan struct{})
	c.mu.Unlock()

	go c.pump()

	// The handshake. A server that will not complete this is not usable, so a
	// failure here closes the subprocess rather than leaving it running.
	if _, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": MCPProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ear", "version": "go"},
	}); err != nil {
		_ = c.Close()
		return err
	}
	if err := c.notify("notifications/initialized", map[string]any{}); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

// Close shuts the connection and the subprocess down. Safe to call more than
// once, and safe on a client that never connected.
func (c *McpClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	stdin, cmd, done := c.stdin, c.cmd, c.pumpDone
	c.mu.Unlock()

	// Closing stdin is how a well-behaved stdio server learns to exit; the
	// pump then sees EOF and unblocks anything still waiting.
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		// Give it a moment to exit on its own, then insist.
		exited := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	c.drainPending("connection closed")
	return nil
}

// ListTools asks the server what it offers.
func (c *McpClient) ListTools(ctx context.Context) ([]McpTool, error) {
	result, err := c.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, mcpErrorf(c.Server.Name, "tools/list", "malformed response: %v", err)
	}

	tools := make([]McpTool, 0, len(payload.Tools))
	for _, t := range payload.Tools {
		params := make([]string, 0, len(t.InputSchema.Properties))
		for name := range t.InputSchema.Properties {
			params = append(params, name)
		}
		sortStrings(params) // map iteration is random; a stable catalogue is not
		tools = append(tools, McpTool{Name: t.Name, Description: t.Description, Parameters: params})
	}
	return tools, nil
}

// CallTool invokes one of the server's tools and returns its text content.
func (c *McpClient) CallTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	result, err := c.request(ctx, "tools/call", map[string]any{
		"name": name, "arguments": arguments,
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return "", mcpErrorf(c.Server.Name, "tools/call", "malformed response from %q: %v", name, err)
	}

	var parts []string
	for _, item := range payload.Content {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if payload.IsError {
		// A tool that reports its own failure is a failure, not a result.
		return "", mcpErrorf(c.Server.Name, "tools/call", "tool %q reported an error: %s", name, text)
	}
	return text, nil
}

// -- transport ---------------------------------------------------------------

// request sends a JSON-RPC request and waits for its response, bounded by ctx
// and the configured timeout.
func (c *McpClient) request(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed || c.stdin == nil {
		c.mu.Unlock()
		return nil, mcpErrorf(c.Server.Name, method, "not connected")
	}
	c.nextID++
	id := c.nextID
	reply := make(chan *rpcMessage, 1)
	c.pending[id] = reply
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(&rpcMessage{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		return nil, &McpError{Server: c.Server.Name, Op: method, Err: err}
	}

	timer := time.NewTimer(c.timeout())
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, &McpError{Server: c.Server.Name, Op: method, Err: ctx.Err()}
	case <-timer.C:
		return nil, mcpErrorf(c.Server.Name, method, "timed out after %s", c.timeout())
	case message := <-reply:
		if message == nil {
			c.mu.Lock()
			cause := c.pumpErr
			c.mu.Unlock()
			if cause == nil {
				cause = errors.New("connection closed")
			}
			return nil, &McpError{Server: c.Server.Name, Op: method, Err: cause}
		}
		if message.Error != nil {
			return nil, mcpErrorf(c.Server.Name, method, "server error %d: %s",
				message.Error.Code, message.Error.Message)
		}
		return message.Result, nil
	}
}

// notify sends a JSON-RPC notification, which by definition has no reply.
func (c *McpClient) notify(method string, params map[string]any) error {
	if err := c.write(&rpcMessage{JSONRPC: "2.0", Method: method, Params: params}); err != nil {
		return &McpError{Server: c.Server.Name, Op: method, Err: err}
	}
	return nil
}

func (c *McpClient) write(message *rpcMessage) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.mu.Lock()
	stdin, closed := c.stdin, c.closed
	c.mu.Unlock()
	if closed || stdin == nil {
		return errors.New("not connected")
	}
	_, err = stdin.Write(append(encoded, '\n'))
	return err
}

// pump is the single reader. It runs for the connection's lifetime, routing
// each frame to whoever is waiting for that id.
func (c *McpClient) pump() {
	c.mu.Lock()
	stdout, done := c.stdout, c.pumpDone
	c.mu.Unlock()
	defer close(done)

	scanner := bufio.NewScanner(stdout)
	// A tool result can be far larger than the default 64 KiB line cap, and a
	// truncated frame would be a confusing parse error rather than an obvious
	// size problem.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var message rpcMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			// A server writing non-JSON to stdout is misbehaving, but it may
			// be a stray log line rather than a broken frame; skip it rather
			// than tearing the connection down.
			continue
		}
		c.deliver(&message)
	}

	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	c.pumpErr = err
	c.mu.Unlock()
	c.drainPending("server stopped responding")
}

// deliver routes one frame. A response nobody is waiting for -- one that
// outlived its caller's deadline, or a server-initiated notification -- is
// dropped, not an error.
func (c *McpClient) deliver(message *rpcMessage) {
	if message.ID == nil {
		return
	}
	c.mu.Lock()
	reply, waiting := c.pending[*message.ID]
	if waiting {
		delete(c.pending, *message.ID)
	}
	c.mu.Unlock()
	if !waiting {
		return
	}
	select {
	case reply <- message:
	default:
	}
}

// drainPending unblocks every waiting caller when the connection ends, so a
// dead server surfaces as an error rather than a hang.
func (c *McpClient) drainPending(string) {
	c.mu.Lock()
	waiting := c.pending
	c.pending = map[int]chan *rpcMessage{}
	c.mu.Unlock()
	for _, reply := range waiting {
		select {
		case reply <- nil:
		default:
		}
	}
}

// commandWords splits a declared launch command into argv, honouring simple
// single and double quoting so a path with a space survives.
func commandWords(command string) []string {
	var words []string
	var current strings.Builder
	var quote rune
	inWord := false

	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, current.String())
	}
	return words
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// -- wiring into the runtime -------------------------------------------------

// ConnectMCP connects to a server the stack declared in memory.md's `## MCP`
// section and binds every tool it advertises, returning the connected client
// so the caller can Close it.
//
// The declaration is the authorization: a name the stack never declared is
// refused, so connecting is the runtime reaching out to what the author
// already named rather than code growing the runtime a capability the
// natural-language authoring does not show. That is the same discipline
// BindTool enforces for native tools, applied one level up -- the author
// declares the *server*, and the server's own catalogue supplies the tools.
//
// Bound MCP tools are ordinary BoundTools, so they run through InvokeTool:
// judged by tool-scoped policies, recorded on the trail with arguments,
// result and duration, and counted against the tool-loop budget.
func (r *Runtime) ConnectMCP(ctx context.Context, name string) (*McpClient, error) {
	if r.Strategy == nil {
		return nil, mcpErrorf(name, "connect", "this runtime has no strategy declaring any MCP servers")
	}
	declared, ok := r.Strategy.McpServerNamed(name)
	if !ok {
		return nil, mcpErrorf(name, "connect",
			"not declared in memory.md's ## MCP section (declared: %s)",
			strings.Join(r.Strategy.mcpServerNames(), ", "))
	}

	client := NewMcpClient(declared)
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}

	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	for _, tool := range tools {
		toolName := tool.Name
		r.ToolBinder.add(&BoundTool{
			// Namespaced by server, so two servers offering a `search` tool
			// stay distinguishable and neither silently shadows the other.
			Name:        declared.Name + "." + toolName,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			Handler: func(args map[string]any) (string, error) {
				return client.CallTool(ctx, toolName, args)
			},
		})
	}

	r.ReasoningLog.Record(Record{
		Stage:  "mcp",
		Inputs: map[string]any{"server": declared.Name, "reach": declared.Reach(), "tools": len(tools)},
		Output: fmt.Sprintf("connected to %q, bound %d tool(s)", declared.Name, len(tools)),
	})
	return client, nil
}

// mcpServerNames lists the declared server names, for a diagnostic that tells
// the author what they could have meant.
func (s *Strategy) mcpServerNames() []string {
	names := make([]string, 0, len(s.McpServers))
	for _, server := range s.McpServers {
		names = append(names, server.Name)
	}
	if len(names) == 0 {
		return []string{"none"}
	}
	return names
}
