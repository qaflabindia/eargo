package ear

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server -- EAR as a running control-plane service: a small HTTP front door
// onto the Kernel, so a fleet of runtime instances can be created, driven and
// observed over the network.
//
// It is the server-side face of the same picture the Kernel draws. The Kernel
// is the scheduler, each instance is a process with its own memory, tenant and
// trail, and the Server is the syscall interface: create an instance, submit
// work to it, ask how it is doing. Zero dependencies -- net/http speaking
// JSON, with the Kernel running the work behind it.
//
// Solid by construction, not by afterthought:
//
//   - Auth. A bearer token, read from EAR_SERVER_TOKEN (never hardcoded) and
//     compared in constant time; every request, including the health check, is
//     refused without it. Unset means open, and the server says so loudly on
//     start -- a development convenience you opt into, not a silent default.
//   - Confinement. Loading a stack is confined under StacksRoot; a path that
//     escapes it is refused. No StacksRoot, no loading paths from the wire at
//     all.
//   - Resilience. Request bodies are capped, malformed JSON is a 400 not a
//     crash, and every handler is wrapped so one bad request can never take
//     the server down.
//   - Testability. The routing is a pure function -- Handle(method, path,
//     body) -> (status, payload) -- so the whole API is exercised without
//     opening a socket.
type Server struct {
	// Kernel schedules and runs everything the server accepts. Required.
	Kernel *Kernel

	// StacksRoot confines which directories may be loaded as instances.
	// Empty forbids creating instances from the wire entirely: a server that
	// can load any path an attacker names is a remote file-read primitive.
	StacksRoot string

	// Token is the bearer token every request must present. Empty falls back
	// to EAR_SERVER_TOKEN at Serve time; still empty leaves the server open,
	// which it announces loudly rather than defaulting to silently.
	Token string

	// Addr is the listen address, defaulting to ":8080".
	Addr string

	// Claim, when set, is the identity every instance's work runs under, so
	// the tenant boundary applies to work arriving over the network.
	//
	// It is deliberately NOT taken from the request body. A caller who can
	// name their own org has no boundary at all -- the claim has to come from
	// the deployment, alongside the token that authenticated the caller.
	Claim *Claim

	startedAt time.Time

	mu sync.Mutex
	// lastIntents remembers what each instance was last asked to do. There is
	// no shared filesystem across a network boundary for the approval.md
	// file-drop convention, so the server is what remembers what a parked
	// cycle was actually asked to do.
	lastIntents map[string]Intent
	lastResults map[string]string
}

// maxBodyBytes caps a request body. Generous for an intent and its context,
// far short of anything that would pressure memory.
const maxBodyBytes = 1 << 20 // 1 MiB

// clientError is a mistake the caller made, carrying the status it deserves.
type clientError struct {
	status  int
	message string
}

func (e *clientError) Error() string { return e.message }

func badRequest(format string, args ...any) *clientError {
	return &clientError{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) *clientError {
	return &clientError{status: http.StatusNotFound, message: fmt.Sprintf(format, args...)}
}

// NewServer builds a server over a kernel, with a kernel created if none is
// given.
func NewServer(kernel *Kernel) *Server {
	if kernel == nil {
		kernel = NewKernel()
	}
	return &Server{
		Kernel:      kernel,
		startedAt:   time.Now(),
		lastIntents: map[string]Intent{},
		lastResults: map[string]string{},
	}
}

func (s *Server) ensure() {
	if s.lastIntents == nil {
		s.lastIntents = map[string]Intent{}
	}
	if s.lastResults == nil {
		s.lastResults = map[string]string{}
	}
	if s.startedAt.IsZero() {
		s.startedAt = time.Now()
	}
	if s.Kernel == nil {
		s.Kernel = NewKernel()
	}
}

// -- routing: a pure function, the whole API, no socket ----------------------

// Handle routes one request to a status and payload. Pure and synchronous --
// the socket layer only translates HTTP to and from this. A caller's mistake
// is its own status; anything unexpected is a 500 with the reason, never an
// unhandled crash.
func (s *Server) Handle(ctx context.Context, method, path string, body map[string]any) (status int, payload any) {
	s.mu.Lock()
	s.ensure()
	s.mu.Unlock()

	// One bad request must never take the control plane down.
	defer func() {
		if r := recover(); r != nil {
			status, payload = http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("panic: %v", r)}
		}
	}()

	rawPath, query, _ := strings.Cut(path, "?")
	parts := splitPath(rawPath)
	// A GET carries no body over most HTTP clients, so query parameters are
	// merged in and handlers need not care which side a value arrived on.
	merged := parseQueryFlat(query)
	for k, v := range body {
		merged[k] = v
	}

	result, err := s.route(ctx, method, parts, merged)
	if err != nil {
		var mistake *clientError
		if errors.As(err, &mistake) {
			return mistake.status, map[string]any{"error": mistake.message}
		}
		return http.StatusInternalServerError, map[string]any{"error": err.Error()}
	}
	return http.StatusOK, result
}

func (s *Server) route(ctx context.Context, method string, parts []string, body map[string]any) (any, error) {
	switch {
	case method == http.MethodGet && equalPath(parts, "health"):
		return s.health(), nil
	case method == http.MethodGet && equalPath(parts, "kernel"):
		return snapshotPayload(s.Kernel.Snapshot()), nil
	case method == http.MethodGet && equalPath(parts, "fleet"):
		return fleetPayload(InspectFleet(s.Kernel, time.Now())), nil
	case equalPath(parts, "instances"):
		switch method {
		case http.MethodGet:
			return map[string]any{"instances": s.Kernel.Instances()}, nil
		case http.MethodPost:
			return s.create(body)
		}
	case len(parts) >= 2 && parts[0] == "instances":
		name := parts[1]
		switch {
		case len(parts) == 2 && method == http.MethodDelete:
			return s.delete(name)
		case len(parts) == 3 && method == http.MethodPost && parts[2] == "submit":
			return s.submit(ctx, name, body)
		case len(parts) == 3 && method == http.MethodPost && parts[2] == "approve":
			return s.approve(ctx, name, body)
		case len(parts) == 3 && method == http.MethodGet && parts[2] == "status":
			return s.status(name)
		case len(parts) == 3 && method == http.MethodGet && parts[2] == "trail":
			return s.trail(name, body)
		}
	}
	return nil, notFound("no route for %s /%s", method, strings.Join(parts, "/"))
}

// -- endpoints ---------------------------------------------------------------

func (s *Server) health() map[string]any {
	snap := s.Kernel.Snapshot()
	return map[string]any{
		"status":     "ok",
		"instances":  len(snap.Instances),
		"pending":    snap.Pending,
		"dispatched": snap.Dispatched,
		"running":    snap.Running,
		"uptime_s":   int(time.Since(s.startedAt).Seconds()),
	}
}

// create loads a stack as a new instance, confined under StacksRoot.
func (s *Server) create(body map[string]any) (any, error) {
	if s.StacksRoot == "" {
		return nil, &clientError{
			status:  http.StatusForbidden,
			message: "this server has no stacks root configured, so it will not load stacks from the wire",
		}
	}
	stack, err := stringField(body, "stack")
	if err != nil {
		return nil, err
	}
	dir, err := s.resolveStack(stack)
	if err != nil {
		return nil, err
	}

	name, _ := body["name"].(string)
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(dir)
	}

	runtime, err := LoadRuntime(dir, "")
	if err != nil {
		return nil, badRequest("loading the stack: %v", err)
	}
	s.Kernel.Register(name, runtime)

	// A stack that authored standing work gets it armed on registration, the
	// same as `ear kernel` does -- the schedule is a property of the stack,
	// not of how it happened to be started.
	armed, err := s.Kernel.Arm(name)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"created": name,
		"stack":   dir,
		"org_id":  runtime.Tenant.OrgID,
		"armed":   len(armed),
	}, nil
}

// resolveStack confines a wire-supplied path under StacksRoot. Both sides are
// resolved through EvalSymlinks where possible, so a symlink inside the root
// cannot be used to step outside it.
func (s *Server) resolveStack(stack string) (string, error) {
	root, err := filepath.Abs(s.StacksRoot)
	if err != nil {
		return "", fmt.Errorf("resolving the stacks root: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	candidate := stack
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", badRequest("unreadable stack path")
	}
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}

	// filepath.Rel is the reliable containment test: a candidate outside root
	// produces a path starting with "..".
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &clientError{
			status:  http.StatusForbidden,
			message: "stack path escapes the configured stacks root",
		}
	}
	return candidate, nil
}

func (s *Server) delete(name string) (any, error) {
	if _, ok := s.Kernel.Instance(name); !ok {
		return nil, notFound("no such instance %q", name)
	}
	s.Kernel.Remove(name)
	s.mu.Lock()
	delete(s.lastIntents, name)
	delete(s.lastResults, name)
	s.mu.Unlock()
	return map[string]any{"deleted": name}, nil
}

// submit runs one intent on an instance and reports the governed outcome. It
// runs synchronously: a caller who asked for a decision wants the decision,
// and the Kernel's scheduling exists for standing work, not request/response.
func (s *Server) submit(ctx context.Context, name string, body map[string]any) (any, error) {
	runtime, ok := s.Kernel.Instance(name)
	if !ok {
		return nil, notFound("no such instance %q", name)
	}
	text, err := stringField(body, "intent")
	if err != nil {
		return nil, err
	}
	intentContext, err := contextField(body)
	if err != nil {
		return nil, err
	}

	intent := NewIntent(text, intentContext)
	s.mu.Lock()
	s.lastIntents[name] = intent
	s.mu.Unlock()

	return s.reason(ctx, name, runtime, intent, nil)
}

// approve releases a cycle a gate previously parked. Exchange's approval.md
// file-drop convention assumes the human and the runtime share a disk, which
// is not true across a network boundary; this is the same release spoken over
// the wire, resubmitting the instance's last intent with the verdict attached.
func (s *Server) approve(ctx context.Context, name string, body map[string]any) (any, error) {
	runtime, ok := s.Kernel.Instance(name)
	if !ok {
		return nil, notFound("no such instance %q", name)
	}

	s.mu.Lock()
	intent, remembered := s.lastIntents[name]
	s.mu.Unlock()
	if !remembered {
		return nil, badRequest("instance %q has no parked intent to approve", name)
	}

	verdict := true
	if raw, present := body["verdict"]; present {
		parsed, err := boolValue(raw)
		if err != nil {
			return nil, badRequest("verdict must be true or false")
		}
		verdict = parsed
	}
	approver, _ := body["approver"].(string)
	note, _ := body["note"].(string)

	return s.reason(ctx, name, runtime, intent, &ApprovalVerdict{
		Verdict:  &verdict,
		Approver: approver,
		Note:     note,
	})
}

// reason runs the cycle and maps its outcome onto the governed vocabulary the
// rest of EAR uses: decided, blocked, awaiting approval. A refusal is a normal
// answer with a 200, not an error -- the request succeeded; governance said no.
func (s *Server) reason(ctx context.Context, name string, runtime *Runtime, intent Intent, approval *ApprovalVerdict) (any, error) {
	if s.Claim != nil {
		ctx = WithClaim(ctx, *s.Claim)
	}

	decision, err := runtime.Reason(ctx, intent, approval)
	if err == nil {
		summary := fmt.Sprint(decision)
		s.mu.Lock()
		s.lastResults[name] = summary
		s.mu.Unlock()
		return map[string]any{"instance": name, "outcome": "decided", "decision": summary}, nil
	}

	var (
		parked   *ApprovalRequiredError
		violated *PolicyViolationError
		boundary *TenantBoundaryError
	)
	switch {
	case errors.As(err, &parked):
		return map[string]any{
			"instance": name, "outcome": "awaiting_approval",
			"policies": policyNames(parked.Policies), "detail": parked.Error(),
		}, nil
	case errors.As(err, &violated):
		return map[string]any{
			"instance": name, "outcome": "blocked",
			"policies": policyNames(violated.Policies), "detail": violated.Error(),
		}, nil
	case errors.As(err, &boundary):
		return map[string]any{
			"instance": name, "outcome": "blocked",
			"detail": boundary.Error(),
		}, nil
	}
	return nil, err
}

func (s *Server) status(name string) (any, error) {
	runtime, ok := s.Kernel.Instance(name)
	if !ok {
		return nil, notFound("no such instance %q", name)
	}
	s.mu.Lock()
	last, hasLast := s.lastIntents[name]
	result := s.lastResults[name]
	s.mu.Unlock()

	payload := map[string]any{
		"instance":     name,
		"org_id":       runtime.Tenant.OrgID,
		"processes":    len(runtime.Processes),
		"policies":     len(runtime.Policies),
		"observations": runtime.Experience.Observations,
	}
	if hasLast {
		payload["last_intent"] = last.Text
	}
	if result != "" {
		payload["last_decision"] = result
	}
	return payload, nil
}

func (s *Server) trail(name string, body map[string]any) (any, error) {
	runtime, ok := s.Kernel.Instance(name)
	if !ok {
		return nil, notFound("no such instance %q", name)
	}
	limit := 50
	if raw, present := body["limit"]; present {
		parsed, err := intValue(raw)
		if err != nil || parsed <= 0 {
			return nil, badRequest("limit must be a positive integer")
		}
		limit = parsed
	}

	var records []map[string]any
	for record := range runtime.ReasoningLog.Records() {
		records = append(records, map[string]any{
			"stage": record.Stage, "output": record.Output, "cycle": record.Cycle,
		})
	}
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	intact, detail := runtime.ReasoningLog.Verify()
	return map[string]any{
		"instance": name, "records": records,
		"chain_intact": intact, "chain_detail": detail,
	}, nil
}

// -- the socket layer --------------------------------------------------------

// ServeHTTP translates HTTP to and from Handle: authenticate, cap the body,
// decode JSON, and render the result. It holds no routing logic of its own.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}

	body := map[string]any{}
	if r.Body != nil && r.ContentLength != 0 {
		limited := http.MaxBytesReader(w, r.Body, maxBodyBytes)
		decoded := map[string]any{}
		if err := json.NewDecoder(limited).Decode(&decoded); err != nil {
			// A malformed body is the caller's mistake, not a crash.
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed JSON body: " + err.Error()})
			return
		}
		body = decoded
	}

	status, payload := s.Handle(r.Context(), r.Method, r.RequestURI, body)
	writeJSON(w, status, payload)
}

// authorized checks the bearer token in constant time. An unset token leaves
// the server open -- announced on start, never silent.
func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.Token)) == 1
}

// Serve runs the HTTP front door and the Kernel's idle loop together until ctx
// is cancelled, then shuts the listener down gracefully and lets in-flight
// cycles finish. It is the network form of `ear kernel`.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	s.ensure()
	s.mu.Unlock()

	if s.Token == "" {
		s.Token = os.Getenv("EAR_SERVER_TOKEN")
	}
	if s.Token == "" {
		fmt.Fprintln(os.Stderr,
			"ear server: WARNING -- no EAR_SERVER_TOKEN set; every request will be accepted unauthenticated")
	}
	if s.StacksRoot == "" {
		fmt.Fprintln(os.Stderr,
			"ear server: no stacks root configured; creating instances over the wire is disabled")
	}
	addr := s.Addr
	if addr == "" {
		addr = ":8080"
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The kernel runs the standing work while the server answers requests.
	kernelDone := make(chan error, 1)
	go func() { kernelDone <- s.Kernel.Run(ctx) }()

	serverDone := make(chan error, 1)
	go func() { serverDone <- httpServer.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := httpServer.Shutdown(shutdownCtx)
		<-kernelDone // let in-flight cycles finish writing
		return err
	case err := <-serverDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// -- helpers -----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoded, err := json.Marshal(payload)
	if err != nil {
		// Never leave a caller hanging on an unencodable payload.
		encoded = []byte(`{"error":"response could not be encoded"}`)
	}
	_, _ = w.Write(encoded)
	_, _ = w.Write([]byte("\n"))
}

func splitPath(path string) []string {
	var parts []string
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment != "" {
			parts = append(parts, segment)
		}
	}
	return parts
}

func equalPath(parts []string, want ...string) bool {
	if len(parts) != len(want) {
		return false
	}
	for i := range parts {
		if parts[i] != want[i] {
			return false
		}
	}
	return true
}

// parseQueryFlat reads a query string into flat values, coercing them the same
// way the markdown loader coerces authored values so ?limit=50 arrives as a
// number rather than a string.
func parseQueryFlat(query string) map[string]any {
	out := map[string]any{}
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		if key == "" {
			continue
		}
		out[key] = Coerce(value)
	}
	return out
}

func stringField(body map[string]any, key string) (string, error) {
	raw, present := body[key]
	if !present {
		return "", badRequest("%q is required", key)
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", badRequest("%q must be a non-empty string", key)
	}
	return value, nil
}

func contextField(body map[string]any) (map[string]any, error) {
	raw, present := body["context"]
	if !present || raw == nil {
		return map[string]any{}, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, badRequest("\"context\" must be an object")
	}
	return value, nil
}

func boolValue(raw any) (bool, error) {
	switch v := raw.(type) {
	case bool:
		return v, nil
	case string:
		return strconv.ParseBool(v)
	}
	return false, fmt.Errorf("not a boolean")
}

func intValue(raw any) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case float64: // every JSON number decodes as float64
		return int(v), nil
	case string:
		return strconv.Atoi(v)
	}
	return 0, fmt.Errorf("not an integer")
}

// fleetPayload renders the fleet health for the /fleet endpoint -- the same
// distilled model the terminal monitor draws, so the browser board and the
// control-room wall never disagree about whether an instance is well.
func fleetPayload(fleet FleetHealth) map[string]any {
	instances := make([]map[string]any, 0, len(fleet.Instances))
	for _, instance := range fleet.Instances {
		instances = append(instances, map[string]any{
			"name": instance.Name, "status": string(instance.Status), "reason": instance.Reason,
			"freshness": string(instance.Freshness), "cycles": instance.Cycles,
			"calls": instance.Calls, "tokens": instance.Tokens, "dollars": instance.Dollars,
			"blocked": instance.Blocked, "pending": instance.Pending, "failed": instance.Failed,
			"chain_intact": instance.ChainIntact, "spark": instance.Spark,
		})
	}
	return map[string]any{
		"status": string(fleet.Status()), "instances": instances,
		"cycles": fleet.Cycles, "tokens": fleet.Tokens, "dollars": fleet.Dollars,
		"blocked": fleet.Blocked, "pending": fleet.Pending, "failed": fleet.Failed,
		"broken": fleet.Broken,
	}
}

func snapshotPayload(snap Snapshot) map[string]any {
	recent := make([]map[string]any, 0, len(snap.Recent))
	for _, d := range snap.Recent {
		recent = append(recent, map[string]any{
			"task_id": d.TaskID, "instance": d.Instance,
			"status": string(d.Status), "summary": d.Summary,
			"duration_ms": d.Duration.Milliseconds(),
		})
	}
	return map[string]any{
		"instances": snap.Instances, "pending": snap.Pending,
		"in_flight": snap.InFlight, "running": snap.Running,
		"idle_waits": snap.IdleWaits, "dispatched": snap.Dispatched,
		"recent": recent,
	}
}
