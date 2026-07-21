package ear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serverStack lays down a loadable stack under a stacks root and returns the
// root, so confinement can be exercised against a real directory.
func serverStack(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"process.md":  "# Desk\n\n## Handle\n\nHandle requests.\n\nWorkflows: W\n",
		"workflow.md": "## W\n\n1. Decide approve or decline.\n\nPolicies: Loan Amount Cap\n",
		"policy.md":   "# Policies\n\n## Loan Amount Cap\n\nThe loan must not exceed $75,000.\n\nFallback: loan_amount <= 75000\n",
	}
	for file, body := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// newTestServer returns a server with one instance already created.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := serverStack(t, "desk")
	s := NewServer(nil)
	s.StacksRoot = root
	status, payload := s.Handle(context.Background(), http.MethodPost, "/instances",
		map[string]any{"stack": "desk"})
	if status != http.StatusOK {
		t.Fatalf("creating the instance: %d %v", status, payload)
	}
	return s, root
}

func asMap(t *testing.T, payload any) map[string]any {
	t.Helper()
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload is %T, not a map: %v", payload, payload)
	}
	return m
}

func TestServerHealthAndKernelSnapshot(t *testing.T) {
	s, _ := newTestServer(t)

	status, payload := s.Handle(context.Background(), http.MethodGet, "/health", nil)
	if status != http.StatusOK {
		t.Fatalf("health status = %d", status)
	}
	if got := asMap(t, payload)["status"]; got != "ok" {
		t.Errorf("health status field = %v", got)
	}

	status, payload = s.Handle(context.Background(), http.MethodGet, "/kernel", nil)
	if status != http.StatusOK {
		t.Fatalf("kernel status = %d", status)
	}
	if _, ok := asMap(t, payload)["pending"]; !ok {
		t.Error("kernel snapshot should report the queue depth")
	}
}

func TestServerCreateAndListInstances(t *testing.T) {
	s, _ := newTestServer(t)

	status, payload := s.Handle(context.Background(), http.MethodGet, "/instances", nil)
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	instances, _ := asMap(t, payload)["instances"].([]string)
	if len(instances) != 1 || instances[0] != "desk" {
		t.Errorf("want [desk], got %v", instances)
	}
}

func TestServerSubmitReportsGovernedOutcomes(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()

	// A compliant intent decides.
	status, payload := s.Handle(ctx, http.MethodPost, "/instances/desk/submit", map[string]any{
		"intent": "Underwrite a small loan", "context": map[string]any{"loan_amount": 20000.0},
	})
	if status != http.StatusOK {
		t.Fatalf("submit status = %d %v", status, payload)
	}
	if got := asMap(t, payload)["outcome"]; got != "decided" {
		t.Errorf("want decided, got %v", got)
	}

	// A policy block is a normal answer with a 200: the request succeeded,
	// governance said no. It is not a server error.
	status, payload = s.Handle(ctx, http.MethodPost, "/instances/desk/submit", map[string]any{
		"intent": "Underwrite an oversized loan", "context": map[string]any{"loan_amount": 90000.0},
	})
	if status != http.StatusOK {
		t.Fatalf("a governance stop must not be an HTTP error, got %d", status)
	}
	body := asMap(t, payload)
	if body["outcome"] != "blocked" {
		t.Errorf("want blocked, got %v", body["outcome"])
	}
	if !strings.Contains(strings.ToLower(body["detail"].(string)), "loan amount cap") {
		t.Errorf("the block should name the policy, got %v", body["detail"])
	}
}

func TestServerRejectsMalformedSubmissions(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"no intent", map[string]any{}},
		{"empty intent", map[string]any{"intent": "   "}},
		{"intent not a string", map[string]any{"intent": 42}},
		{"context not an object", map[string]any{"intent": "Do it", "context": "loan_amount=1"}},
	} {
		status, _ := s.Handle(ctx, http.MethodPost, "/instances/desk/submit", tc.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", tc.name, status)
		}
	}
}

func TestServerUnknownInstanceAndRoute(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()

	if status, _ := s.Handle(ctx, http.MethodGet, "/instances/ghost/status", nil); status != http.StatusNotFound {
		t.Errorf("unknown instance: want 404, got %d", status)
	}
	if status, _ := s.Handle(ctx, http.MethodGet, "/nope", nil); status != http.StatusNotFound {
		t.Errorf("unknown route: want 404, got %d", status)
	}
	if status, _ := s.Handle(ctx, http.MethodPatch, "/instances", nil); status != http.StatusNotFound {
		t.Errorf("unsupported method: want 404, got %d", status)
	}
}

func TestServerDeleteInstance(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()

	if status, _ := s.Handle(ctx, http.MethodDelete, "/instances/desk", nil); status != http.StatusOK {
		t.Fatalf("delete should succeed")
	}
	if status, _ := s.Handle(ctx, http.MethodGet, "/instances/desk/status", nil); status != http.StatusNotFound {
		t.Errorf("a deleted instance should be gone, got %d", status)
	}
	if status, _ := s.Handle(ctx, http.MethodDelete, "/instances/desk", nil); status != http.StatusNotFound {
		t.Errorf("deleting twice should 404, got %d", status)
	}
}

// -- confinement -------------------------------------------------------------

func TestServerConfinesStackPathsToTheRoot(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()

	for _, escape := range []string{
		"../etc",
		"../../",
		"desk/../../outside",
		"/etc",
	} {
		status, payload := s.Handle(ctx, http.MethodPost, "/instances", map[string]any{"stack": escape})
		if status != http.StatusForbidden {
			t.Errorf("stack %q: want 403, got %d (%v)", escape, status, payload)
		}
	}
}

func TestServerWithNoStacksRootRefusesToLoadAnything(t *testing.T) {
	// A server that can load any path a caller names is a remote file-read
	// primitive; no root configured must mean no loading at all.
	s := NewServer(nil)
	status, _ := s.Handle(context.Background(), http.MethodPost, "/instances",
		map[string]any{"stack": "/etc"})
	if status != http.StatusForbidden {
		t.Errorf("want 403 with no stacks root, got %d", status)
	}
}

// -- auth --------------------------------------------------------------------

func TestServerRefusesEveryRequestWithoutTheToken(t *testing.T) {
	s, _ := newTestServer(t)
	s.Token = "s3cret"

	// Even the health check: an unauthenticated caller learns nothing.
	for _, path := range []string{"/health", "/kernel", "/instances"} {
		recorder := httptest.NewRecorder()
		s.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token: want 401, got %d", path, recorder.Code)
		}
	}

	// The right token gets through.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Authorization", "Bearer s3cret")
	s.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("with the token: want 200, got %d", recorder.Code)
	}

	// A wrong token does not.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	s.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("with a wrong token: want 401, got %d", recorder.Code)
	}
}

// -- the tenant boundary over the wire ---------------------------------------

func TestServerClaimIsNotTakenFromTheRequestBody(t *testing.T) {
	// A caller who can name their own org has no boundary at all. The claim
	// comes from the deployment; a body that tries to assert one is ignored.
	s, _ := newTestServer(t)
	runtime, _ := s.Kernel.Instance("desk")
	runtime.Tenant = Tenant{OrgID: "acme"}
	s.Claim = &Claim{Subject: "svc:server", OrgIDs: []string{"globex"}}

	status, payload := s.Handle(context.Background(), http.MethodPost, "/instances/desk/submit",
		map[string]any{
			"intent":  "Read the loan book",
			"claim":   map[string]any{"subject": "svc:me", "org_ids": []any{"acme"}},
			"subject": "svc:me", "org_ids": []any{"acme"},
		})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	body := asMap(t, payload)
	if body["outcome"] != "blocked" {
		t.Fatalf("a self-asserted claim must not widen the boundary, got %v (%v)", body["outcome"], body["detail"])
	}
	if !strings.Contains(body["detail"].(string), "svc:server") {
		t.Errorf("the deployment's claim should be the one enforced, got %v", body["detail"])
	}
}

func TestServerAuthorizedClaimReachesTheInstance(t *testing.T) {
	s, _ := newTestServer(t)
	runtime, _ := s.Kernel.Instance("desk")
	runtime.Tenant = Tenant{OrgID: "acme"}
	s.Claim = &Claim{Subject: "svc:server", OrgIDs: []string{"acme"}}

	_, payload := s.Handle(context.Background(), http.MethodPost, "/instances/desk/submit",
		map[string]any{"intent": "Underwrite a loan", "context": map[string]any{"loan_amount": 20000.0}})
	if got := asMap(t, payload)["outcome"]; got != "decided" {
		t.Errorf("an authorized claim should decide, got %v", got)
	}
}

// -- approval over the wire --------------------------------------------------

func TestServerApprovalReleasesAParkedCycle(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "desk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("process.md", "# Desk\n\n## Handle\n\nHandle requests.\n\nWorkflows: W\n")
	write("workflow.md", "## W\n\n1. Decide.\n\nPolicies: Large Loan Approval\n")
	write("policy.md", "# Policies\n\n## Large Loan Approval\n\nLoans above $50,000 need a human approval.\n\n"+
		"Fallback: loan_amount <= 50000\nApproval: required\n")

	s := NewServer(nil)
	s.StacksRoot = root
	ctx := context.Background()
	s.Handle(ctx, http.MethodPost, "/instances", map[string]any{"stack": "desk"})

	// The gate parks the cycle.
	_, payload := s.Handle(ctx, http.MethodPost, "/instances/desk/submit", map[string]any{
		"intent": "Underwrite a large loan", "context": map[string]any{"loan_amount": 60000.0},
	})
	if got := asMap(t, payload)["outcome"]; got != "awaiting_approval" {
		t.Fatalf("want awaiting_approval, got %v", got)
	}

	// Approving resubmits the remembered intent with the verdict attached --
	// the file-drop convention spoken over the wire.
	_, payload = s.Handle(ctx, http.MethodPost, "/instances/desk/approve", map[string]any{
		"verdict": true, "approver": "riya", "note": "reviewed",
	})
	if got := asMap(t, payload)["outcome"]; got != "decided" {
		t.Fatalf("an approved gate should decide, got %v (%v)", got, payload)
	}

	// Rejecting blocks it instead.
	s.Handle(ctx, http.MethodPost, "/instances/desk/submit", map[string]any{
		"intent": "Underwrite another large loan", "context": map[string]any{"loan_amount": 60000.0},
	})
	_, payload = s.Handle(ctx, http.MethodPost, "/instances/desk/approve", map[string]any{"verdict": false})
	if got := asMap(t, payload)["outcome"]; got != "blocked" {
		t.Errorf("a rejected gate should block, got %v", got)
	}
}

func TestServerApproveWithNothingParked(t *testing.T) {
	s, _ := newTestServer(t)
	status, _ := s.Handle(context.Background(), http.MethodPost, "/instances/desk/approve",
		map[string]any{"verdict": true})
	if status != http.StatusBadRequest {
		t.Errorf("approving with no parked intent: want 400, got %d", status)
	}
}

// -- trail and status --------------------------------------------------------

func TestServerTrailReportsRecordsAndChain(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	s.Handle(ctx, http.MethodPost, "/instances/desk/submit", map[string]any{
		"intent": "Underwrite a loan", "context": map[string]any{"loan_amount": 20000.0},
	})

	status, payload := s.Handle(ctx, http.MethodGet, "/instances/desk/trail?limit=5", nil)
	if status != http.StatusOK {
		t.Fatalf("trail status = %d", status)
	}
	body := asMap(t, payload)
	records, _ := body["records"].([]map[string]any)
	if len(records) == 0 {
		t.Error("a cycle should leave records on the trail")
	}
	if len(records) > 5 {
		t.Errorf("limit=5 should cap the records, got %d", len(records))
	}
	if body["chain_intact"] != true {
		t.Errorf("the chain should verify, got %v (%v)", body["chain_intact"], body["chain_detail"])
	}

	if status, _ := s.Handle(ctx, http.MethodGet, "/instances/desk/trail?limit=0", nil); status != http.StatusBadRequest {
		t.Errorf("limit=0 should be rejected, got %d", status)
	}
}

func TestServerStatusReportsTheInstance(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	s.Handle(ctx, http.MethodPost, "/instances/desk/submit", map[string]any{
		"intent": "Underwrite a loan", "context": map[string]any{"loan_amount": 20000.0},
	})

	_, payload := s.Handle(ctx, http.MethodGet, "/instances/desk/status", nil)
	body := asMap(t, payload)
	if body["last_intent"] != "Underwrite a loan" {
		t.Errorf("status should remember the last intent, got %v", body["last_intent"])
	}
	if body["org_id"] != DefaultOrgID {
		t.Errorf("status should report the org, got %v", body["org_id"])
	}
}

// -- the socket layer --------------------------------------------------------

func TestServerRejectsMalformedJSONWithoutCrashing(t *testing.T) {
	s, _ := newTestServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/instances/desk/submit",
		strings.NewReader("{not json"))
	s.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON: want 400, got %d", recorder.Code)
	}
	// And the server still answers afterwards.
	recorder = httptest.NewRecorder()
	s.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("the server should survive a bad request, got %d", recorder.Code)
	}
}

func TestServerCapsRequestBodies(t *testing.T) {
	s, _ := newTestServer(t)
	huge := `{"intent":"` + strings.Repeat("a", maxBodyBytes+1024) + `"}`
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/instances/desk/submit",
		strings.NewReader(huge)))
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("an oversized body should be refused, got %d", recorder.Code)
	}
}

func TestServerEndToEndOverHTTP(t *testing.T) {
	s, _ := newTestServer(t)
	s.Token = "s3cret"
	httpServer := httptest.NewServer(s)
	defer httpServer.Close()

	body := strings.NewReader(`{"intent":"Underwrite a loan","context":{"loan_amount":20000}}`)
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/instances/desk/submit", body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer s3cret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	if decoded["outcome"] != "decided" {
		t.Errorf("want decided over the wire, got %v", decoded)
	}
}
