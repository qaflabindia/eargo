package ear

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func tenantRuntime(orgID string) *Runtime {
	r := NewRuntime("Boundary Runtime")
	r.Tenant = Tenant{OrgID: orgID}
	workflow := (&Workflow{Name: "Work"}).AddPersona(&Persona{Name: "Analyst", Instructions: "Analyse."})
	r.AddProcess((&Process{Name: "Process", Description: "Do the work."}).AddWorkflow(workflow))
	return r
}

func TestClaimMayActAs(t *testing.T) {
	claim := Claim{Subject: "svc:nightly", OrgIDs: []string{"acme", "acme-eu"}}
	if !claim.MayActAs("acme-eu") {
		t.Error("claim authorized for acme-eu should be able to act as it")
	}
	if claim.MayActAs("globex") {
		t.Error("claim should not act as an org it was never granted")
	}
	if err := claim.Require("acme"); err != nil {
		t.Errorf("Require on an authorized org: %v", err)
	}
}

func TestClaimRequireNamesTheAuthorizedOrgs(t *testing.T) {
	claim := Claim{Subject: "svc:nightly", OrgIDs: []string{"acme"}}
	err := claim.Require("globex")
	var boundary *TenantBoundaryError
	if !errors.As(err, &boundary) {
		t.Fatalf("want *TenantBoundaryError, got %T (%v)", err, err)
	}
	// The refusal has to be diagnosable: who, what they wanted, what they hold.
	for _, want := range []string{"svc:nightly", "globex", "acme"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q omits %q", err.Error(), want)
		}
	}
}

func TestReasonRefusesForeignClaim(t *testing.T) {
	r := tenantRuntime("acme")
	ctx := WithClaim(context.Background(), Claim{Subject: "svc:intruder", OrgIDs: []string{"globex"}})

	_, err := r.Reason(ctx, NewIntent("Read the loan book.", nil), nil)

	var boundary *TenantBoundaryError
	if !errors.As(err, &boundary) {
		t.Fatalf("want *TenantBoundaryError, got %T (%v)", err, err)
	}
	if boundary.OrgID != "acme" {
		t.Errorf("boundary should name the runtime's org, got %q", boundary.OrgID)
	}
}

func TestRefusedClaimIsRecordedOnTheTrail(t *testing.T) {
	r := tenantRuntime("acme")
	ctx := WithClaim(context.Background(), Claim{Subject: "svc:intruder", OrgIDs: []string{"globex"}})

	_, _ = r.Reason(ctx, NewIntent("Read the loan book.", nil), nil)

	var found *Record
	for rec := range r.ReasoningLog.Records() {
		if rec.Stage == "tenant" {
			r := rec
			found = &r
		}
	}
	if found == nil {
		t.Fatal("an attempted cross-tenant access must reach the audit trail")
	}
	if !strings.Contains(found.Output, "REFUSED") {
		t.Errorf("tenant record should say it refused, got %q", found.Output)
	}
	if found.Inputs["subject"] != "svc:intruder" {
		t.Errorf("tenant record should name the subject, got %v", found.Inputs["subject"])
	}
}

func TestReasonAllowsAuthorizedClaim(t *testing.T) {
	r := tenantRuntime("acme")
	ctx := WithClaim(context.Background(), Claim{Subject: "svc:nightly", OrgIDs: []string{"acme"}})

	decision, err := r.Reason(ctx, NewIntent("Grade the applications.", nil), nil)
	if err != nil {
		t.Fatalf("an authorized claim must not be refused: %v", err)
	}
	if decision == nil {
		t.Error("an authorized cycle should still produce a decision")
	}
}

func TestNoClaimIsNotAViolation(t *testing.T) {
	// The "off unless declared" posture: a stack never called with a claim
	// behaves exactly as it always has.
	r := tenantRuntime("acme")
	if _, err := r.Reason(context.Background(), NewIntent("Grade the applications.", nil), nil); err != nil {
		t.Fatalf("a cycle with no claim must not be refused: %v", err)
	}
}

func TestClaimFromReportsAbsence(t *testing.T) {
	if _, ok := ClaimFrom(context.Background()); ok {
		t.Error("a bare context carries no claim")
	}
	if _, ok := ClaimFrom(nil); ok { //nolint:staticcheck // a nil ctx must not panic
		t.Error("a nil context carries no claim")
	}
	ctx := WithClaim(context.Background(), Claim{Subject: "svc:a", OrgIDs: []string{"acme"}})
	claim, ok := ClaimFrom(ctx)
	if !ok || claim.Subject != "svc:a" {
		t.Errorf("claim did not survive the context: %v %v", claim, ok)
	}
}

func TestClaimAuthorizedForNothingIsRefused(t *testing.T) {
	// Distinct from "no claim supplied": an empty claim was supplied, and it
	// grants nothing.
	r := tenantRuntime("acme")
	ctx := WithClaim(context.Background(), Claim{Subject: "svc:stripped"})
	_, err := r.Reason(ctx, NewIntent("Read the loan book.", nil), nil)
	var boundary *TenantBoundaryError
	if !errors.As(err, &boundary) {
		t.Fatalf("an empty claim must be refused, got %T (%v)", err, err)
	}
}
