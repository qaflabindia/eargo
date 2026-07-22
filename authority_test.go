package ear

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// agentRuntime is a minimal governed runtime with envelope enforcement wired.
func agentRuntime(t *testing.T) (*Runtime, *EnvelopeRegistry) {
	t.Helper()
	r := NewRuntime("Enterprise Runtime")
	workflow := (&Workflow{Name: "Work"}).AddPersona(&Persona{Name: "Analyst", Instructions: "Analyse."})
	r.AddProcess((&Process{Name: "Process", Description: "Do the work."}).AddWorkflow(workflow))
	registry := NewEnvelopeRegistry()
	EnforceEnvelopes(r, registry, "")
	return r, registry
}

// actingAs is an intent naming an acting agent, optionally a scope and tier.
func actingAs(agent, scope string, tier int) Intent {
	ctx := map[string]any{"agent": agent}
	if scope != "" {
		ctx["scope"] = scope
	}
	if tier != 0 {
		ctx["autonomy_tier"] = tier
	}
	return NewIntent("Do the work.", ctx)
}

func TestHumanInitiatedIntentIsNotGatedByEnvelopes(t *testing.T) {
	// Off unless declared: an intent that names no acting agent is not
	// applicable, exactly like Claim and Tenant.
	r, _ := agentRuntime(t)
	if _, err := r.Reason(context.Background(), NewIntent("Do the work.", nil), nil); err != nil {
		t.Fatalf("a human-initiated intent must not be gated: %v", err)
	}
}

func TestUncertifiedAgentIsBlocked(t *testing.T) {
	r, _ := agentRuntime(t)
	_, err := r.Reason(context.Background(), actingAs("svc:rogue", "", 0), nil)
	var pv *PolicyViolationError
	if !errors.As(err, &pv) {
		t.Fatalf("an uncertified agent must be blocked, got %T (%v)", err, err)
	}
}

func TestCertifiedAgentInScopeIsAuthorized(t *testing.T) {
	r, registry := agentRuntime(t)
	registry.Certify("svc:nightly", []string{"underwrite"}, 2, 0.8, "")

	if _, err := r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 1), nil); err != nil {
		t.Fatalf("a certified, in-scope, in-tier agent should be authorized: %v", err)
	}
}

func TestScopeAndTierAreEnforced(t *testing.T) {
	r, registry := agentRuntime(t)
	registry.Certify("svc:nightly", []string{"underwrite"}, 1, 0.8, "")

	// Out of scope.
	_, err := r.Reason(context.Background(), actingAs("svc:nightly", "wire_transfer", 1), nil)
	if err == nil {
		t.Error("an out-of-scope action must be blocked")
	}
	// Above tier.
	_, err = r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 5), nil)
	if err == nil {
		t.Error("an above-tier action must be blocked")
	}
}

func TestRevocationIsImmediate(t *testing.T) {
	// The property the whole design exists for: the registry is consulted live,
	// so a revocation between two cycles is enforced on the very next one.
	r, registry := agentRuntime(t)
	registry.Certify("svc:nightly", []string{"underwrite"}, 2, 0.8, "")

	if _, err := r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 1), nil); err != nil {
		t.Fatalf("first cycle should pass: %v", err)
	}

	if err := registry.Revoke("svc:nightly", "credential compromised"); err != nil {
		t.Fatal(err)
	}

	_, err := r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 1), nil)
	var pv *PolicyViolationError
	if !errors.As(err, &pv) {
		t.Fatalf("the next cycle after revocation must be blocked, got %T (%v)", err, err)
	}
}

func TestSuspendAndReinstate(t *testing.T) {
	r, registry := agentRuntime(t)
	registry.Certify("svc:nightly", []string{"underwrite"}, 2, 0.8, "")
	registry.Suspend("svc:nightly", "under review")

	if _, err := r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 1), nil); err == nil {
		t.Error("a suspended agent must be blocked")
	}
	registry.Reinstate("svc:nightly", "review cleared")
	if _, err := r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 1), nil); err != nil {
		t.Errorf("a reinstated agent should act again: %v", err)
	}
}

func TestProbationaryAgentIsAuthorizedButFlagged(t *testing.T) {
	r, registry := agentRuntime(t)
	registry.Certify("svc:nightly", []string{"underwrite"}, 2, 0.8, "")
	registry.Probation("svc:nightly", "recent anomaly")

	if _, err := r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 1), nil); err != nil {
		t.Fatalf("a probationary agent is still authorized: %v", err)
	}
	// The flag is on the record for an adversarial pass to pick up.
	ok, reason := registry.Authorized("svc:nightly", "underwrite", 1)
	if !ok || !strings.Contains(reason, "probation") {
		t.Errorf("probation should authorize-but-flag, got ok=%v reason=%q", ok, reason)
	}
}

// -- the non-waivable floor ---------------------------------------------------

func TestFloorIsNotModelWaivable(t *testing.T) {
	// A judge that waves everything through (the most permissive model there
	// could be) must still not resurrect a revoked credential -- the floor is
	// decided before the judge is ever consulted.
	r, registry := agentRuntime(t)
	r.PolicyJudge = alwaysCompliesJudge{}
	registry.Certify("svc:nightly", []string{"underwrite"}, 2, 0.8, "")
	registry.Revoke("svc:nightly", "compromised")

	_, err := r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 1), nil)
	if err == nil {
		t.Fatal("a permissive judge must not be able to waive a revoked envelope's floor")
	}
}

func TestScopeIsJudgedAboveTheFloor(t *testing.T) {
	// Above the floor, a permissive judge DOES get to decide -- that is the
	// reason-first half. With an always-complies judge, an out-of-scope request
	// on a live envelope passes, proving scope/tier is delegated to the judge
	// rather than hard-coded.
	r, registry := agentRuntime(t)
	r.PolicyJudge = alwaysCompliesJudge{}
	registry.Certify("svc:nightly", []string{"underwrite"}, 1, 0.8, "")

	if _, err := r.Reason(context.Background(), actingAs("svc:nightly", "wire_transfer", 9), nil); err != nil {
		t.Errorf("above the floor the judge decides; a permissive one should allow: %v", err)
	}
}

type alwaysCompliesJudge struct{}

func (alwaysCompliesJudge) Judge(_ context.Context, _ *Policy, _ map[string]any) (bool, string, error) {
	return true, "permissive judge: everything complies", nil
}

// -- tamper evidence ----------------------------------------------------------

func TestTamperedEnvelopeFailsTheFloor(t *testing.T) {
	registry := NewEnvelopeRegistry()
	registry.Certify("svc:nightly", []string{"underwrite"}, 2, 0.8, "")

	// Forge the record directly: flip a suspended standing back to active
	// without re-signing, as an attacker editing stored state would.
	e := registry.Get("svc:nightly")
	e.Status = EnvelopeActive
	e.MaxAutonomyTier = 99 // grant themselves more authority
	// (signature left stale)

	if e.SignatureValid("") {
		t.Fatal("a hand-edited record must not still verify")
	}
	if ok, reason := e.Floor(""); ok {
		t.Errorf("a tampered envelope must fail the floor, got ok reason=%q", reason)
	}
}

func TestSignatureCoversAuthorityFields(t *testing.T) {
	e := (&CapabilityEnvelope{Agent: "a", Certified: true, Scopes: []string{"x"}, MaxAutonomyTier: 1, Status: EnvelopeActive}).Sign("")
	if !e.SignatureValid("") {
		t.Fatal("a freshly signed envelope should verify")
	}
	// Each authority-bearing change must break the signature.
	for _, mutate := range []func(*CapabilityEnvelope){
		func(e *CapabilityEnvelope) { e.Scopes = []string{"x", "y"} },
		func(e *CapabilityEnvelope) { e.MaxAutonomyTier = 2 },
		func(e *CapabilityEnvelope) { e.Status = EnvelopeSuspended },
		func(e *CapabilityEnvelope) { e.Certified = false },
	} {
		clone := *e
		clone.Scopes = append([]string{}, e.Scopes...)
		mutate(&clone)
		if clone.SignatureValid("") {
			t.Error("an authority-field change should break the signature")
		}
	}
	// Trust score is not authority-bearing: changing it does not break the sig.
	clone := *e
	clone.TrustScore = 0.99
	if !clone.SignatureValid("") {
		t.Error("trust score is not covered by the signature")
	}
}

func TestKeyedSignatureNeedsTheSecret(t *testing.T) {
	e := (&CapabilityEnvelope{Agent: "a", Certified: true, Status: EnvelopeActive}).Sign("s3cret")
	if e.SignatureValid("") || e.SignatureValid("wrong") {
		t.Error("a keyed signature must not verify without the right secret")
	}
	if !e.SignatureValid("s3cret") {
		t.Error("the right secret should verify")
	}
}

func TestSecretReadFromEnvVarByNameOnly(t *testing.T) {
	t.Setenv("EAR_AUTH_KEY", "the-secret")
	registry := NewEnvelopeRegistry()
	registry.SecretEnvVar = "EAR_AUTH_KEY"
	registry.Certify("svc:a", []string{"x"}, 1, 0.5, "")

	e := registry.Get("svc:a")
	if !e.SignatureValid("the-secret") {
		t.Error("the registry should sign with the env-var secret")
	}
	if registry.Secret() != "the-secret" {
		t.Error("Secret should read the env var by name")
	}
}

// -- persistence across backends ---------------------------------------------

func TestRegistryPersistsAndReloads(t *testing.T) {
	backend, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := &EnvelopeRegistry{Backend: backend, StateName: "authority_envelopes"}
	registry.Certify("svc:nightly", []string{"underwrite", "report"}, 2, 0.8, "2026-01-01")
	registry.Probation("svc:nightly", "watch")

	// A fresh registry over the same backend sees the persisted state.
	reloaded := &EnvelopeRegistry{Backend: backend, StateName: "authority_envelopes"}
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	e := reloaded.Get("svc:nightly")
	if e == nil {
		t.Fatal("the certified agent did not survive persistence")
	}
	if Normalize(e.Status) != EnvelopeProbation {
		t.Errorf("standing did not persist, got %q", e.Status)
	}
	if len(e.Scopes) != 2 {
		t.Errorf("scopes did not persist, got %v", e.Scopes)
	}
	// And the reloaded record still verifies.
	if !e.SignatureValid("") {
		t.Error("a persisted envelope should still verify")
	}
}

func TestRegistryPersistsToSQLBackend(t *testing.T) {
	// The registry is backend-agnostic: the same JSON blob lives in a database
	// via the SQLBackend just as it does in a file.
	backend, err := NewSQLBackend(openMemDB(t), "authority")
	if err != nil {
		t.Fatal(err)
	}
	registry := &EnvelopeRegistry{Backend: backend, StateName: "authority_envelopes"}
	registry.Certify("svc:nightly", []string{"underwrite"}, 2, 0.8, "")

	reloaded := &EnvelopeRegistry{Backend: backend, StateName: "authority_envelopes"}
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load from SQL: %v", err)
	}
	if reloaded.Get("svc:nightly") == nil {
		t.Error("the envelope did not survive the database round trip")
	}
}

func TestTransitionOnAbsentAgentErrors(t *testing.T) {
	registry := NewEnvelopeRegistry()
	if err := registry.Revoke("svc:ghost", "x"); err == nil {
		t.Error("revoking an uncertified agent should error, not silently succeed")
	}
}

// -- the audit spine ----------------------------------------------------------

func TestTransitionsLandOnTheAuditSpine(t *testing.T) {
	r, registry := agentRuntime(t)
	registry.Certify("svc:nightly", []string{"underwrite"}, 2, 0.8, "")
	registry.Revoke("svc:nightly", "compromised")

	var certified, revoked bool
	for rec := range r.ReasoningLog.Records() {
		if rec.Stage == "certification" {
			if strings.Contains(rec.Output, "active") {
				certified = true
			}
			if strings.Contains(rec.Output, "revoked") {
				revoked = true
			}
		}
	}
	if !certified || !revoked {
		t.Errorf("certification and revocation must land on the audit spine (certified=%v revoked=%v)", certified, revoked)
	}
}

// -- race safety --------------------------------------------------------------

func TestEnvelopeGateIsRaceSafeUnderConcurrentGovern(t *testing.T) {
	// The real concurrency the gate faces is the intra-cycle policy fan-out:
	// govern judges every policy in parallel over one shared context. A gate
	// that mutated that context would race the other policies' judges reading
	// it. Stacking several policies beside the envelope one makes the fan-out
	// genuinely parallel; running it under -race proves the gate's
	// copy-don't-mutate discipline holds.
	//
	// (Whole cycles are NOT run concurrently on one runtime here -- that would
	// race Memory by design, which is exactly why the kernel serializes cycles
	// per instance.)
	r, registry := agentRuntime(t)
	registry.Certify("svc:nightly", []string{"underwrite"}, 3, 0.8, "")
	for i := 0; i < 8; i++ {
		r.AddPolicy(&Policy{Name: fmt.Sprintf("Cap %d", i), FallbackExpression: "true"})
	}

	for i := 0; i < 20; i++ {
		if _, err := r.Reason(context.Background(), actingAs("svc:nightly", "underwrite", 2), nil); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
}
