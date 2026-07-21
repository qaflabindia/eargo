package ear

import (
	"context"
	"fmt"
	"strings"
)

// Identity -- who is calling, and which Tenant they may act as.
//
// Tenant (tenant.go) is the org boundary a Runtime instance belongs to; it
// carries only which org's data the instance's activity belongs to, never who
// a caller is. Claim is the other half: a caller's verified subject plus the
// org ids they are authorized to act as. Authentication -- how a caller proved
// who they are -- is the transport's concern; this is authorization only.
//
// The boundary is checked where work actually reaches a Tenant's data: at the
// top of Runtime.Reason, before the pipeline runs. A Kernel task carries its
// claim on the dispatch context, so scheduled work gets the same check as a
// direct call.
//
// No claim supplied is not a violation. This is the same "off unless declared"
// posture as Tenant itself: a runtime never authored with a tenant.md, and
// never called with a claim, behaves exactly as it always has.

// TenantBoundaryError is returned when a Claim tries to act as an org it is
// not authorized for. It is a refusal, like a violated Policy or a parked
// approval gate -- so a Kernel task lands blocked, not failed.
type TenantBoundaryError struct {
	Subject string
	OrgID   string
	Allowed []string
}

func (e *TenantBoundaryError) Error() string {
	allowed := strings.Join(e.Allowed, ", ")
	if allowed == "" {
		allowed = "none"
	}
	return fmt.Sprintf("claim %q may not act as org %q (authorized for: %s)", e.Subject, e.OrgID, allowed)
}

// Claim is a caller's verified identity plus the org ids they may act as.
type Claim struct {
	Subject string
	OrgIDs  []string
}

// MayActAs reports whether this claim is authorized to act within orgID.
func (c Claim) MayActAs(orgID string) bool {
	for _, id := range c.OrgIDs {
		if id == orgID {
			return true
		}
	}
	return false
}

// Require returns a *TenantBoundaryError if this claim may not act as orgID --
// the enforcement call Runtime.Reason makes at its boundary.
func (c Claim) Require(orgID string) error {
	if c.MayActAs(orgID) {
		return nil
	}
	return &TenantBoundaryError{Subject: c.Subject, OrgID: orgID, Allowed: c.OrgIDs}
}

// claimKey is the unexported context key the claim travels under, so no other
// package can collide with it or forge one.
type claimKey struct{}

// WithClaim returns a context carrying the caller's claim.
//
// The Python package threads the claim as a parameter through reason() and
// Kernel.submit(). In Go the request-scoped carrier is the context, which is
// already threaded through every stage, seam, tool call and subagent spawn --
// so the boundary travels with the work rather than being re-passed at each
// hop, and adding it broke no existing signature.
func WithClaim(ctx context.Context, claim Claim) context.Context {
	return context.WithValue(ctx, claimKey{}, claim)
}

// ClaimFrom returns the claim carried by ctx, if any. The second result
// distinguishes "no claim supplied" (permitted) from a claim authorized for
// nothing (refused).
func ClaimFrom(ctx context.Context) (Claim, bool) {
	if ctx == nil {
		return Claim{}, false
	}
	claim, ok := ctx.Value(claimKey{}).(Claim)
	return claim, ok
}

// checkTenantBoundary enforces any claim on ctx against the runtime's tenant.
// A refusal is recorded on the trail before it is returned: an attempted
// cross-tenant access is exactly the kind of thing an enterprise audit spine
// exists to capture, so it is never a silent rejection.
func (r *Runtime) checkTenantBoundary(ctx context.Context) error {
	claim, ok := ClaimFrom(ctx)
	if !ok {
		return nil
	}
	err := claim.Require(r.Tenant.OrgID)
	if err == nil {
		return nil
	}
	r.ReasoningLog.Record(Record{
		Stage:  "tenant",
		Inputs: map[string]any{"subject": claim.Subject, "org_id": r.Tenant.OrgID, "authorized_for": claim.OrgIDs},
		Output: "REFUSED -- " + err.Error(),
	})
	return err
}
