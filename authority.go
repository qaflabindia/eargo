package ear

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Authority -- capability envelopes: the authority model for every non-human
// actor in the enterprise.
//
// A stack governs *whether* an action is allowed. This governs *who may act at
// all*. An agent -- a spawned persona, an MCP-attached command centre, an
// evolved workflow -- must hold a certified capability envelope before the
// runtime's one governance gate will pass its intents. Certification, trust
// scoring, probation, suspension and revocation are envelope state
// transitions; enforcement is an ordinary runtime-scope Policy whose judgment
// consults the live registry. Revocation is therefore immediate: the registry
// is updated, and the very next Reason() call fails the gate.
//
// The division EAR draws everywhere applies here too. The floor -- whether a
// certified, un-withdrawn, untampered envelope exists at all -- is absolute
// and never model-waivable: the model is never asked to reconsider a revoked
// credential, exactly as it is never asked to waive a human approval. Above
// the floor, whether the granted scopes and tier reach the requested action is
// a judgment: the model reasons over the envelope facts when one is bound, and
// a deterministic check decides offline.

// Envelope standing.
const (
	EnvelopeActive    = "active"
	EnvelopeProbation = "probation"
	EnvelopeSuspended = "suspended"
	EnvelopeRevoked   = "revoked"
)

// The context keys an intent may name its acting agent, scope and tier under.
var (
	actorKeys = []string{"agent", "actor", "agent_id", "acting_agent"}
	scopeKeys = []string{"scope", "capability", "required_scope"}
	tierKeys  = []string{"autonomy_tier", "tier", "required_tier"}
)

// firstString returns the first non-empty context value under any of keys.
func firstString(context map[string]any, keys []string) string {
	for _, key := range keys {
		if v, ok := context[key]; ok && v != nil && v != "" {
			return fmt.Sprint(v)
		}
	}
	return ""
}

// firstValue returns the first present, non-empty context value under keys,
// preserving its type (so a numeric tier stays numeric for the fallback).
func firstValue(context map[string]any, keys []string) any {
	for _, key := range keys {
		if v, ok := context[key]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}

// CapabilityEnvelope is one non-human actor's authority record.
type CapabilityEnvelope struct {
	Agent           string   `json:"agent"`
	Certified       bool     `json:"certified"`
	Scopes          []string `json:"scopes"`
	MaxAutonomyTier int      `json:"max_autonomy_tier"`
	Status          string   `json:"status"`
	TrustScore      float64  `json:"trust_score"`
	IssuedAt        string   `json:"issued_at"`
	Signature       string   `json:"signature"`
}

// IsActive reports whether the envelope is certified and in active standing.
func (e *CapabilityEnvelope) IsActive() bool {
	return e.Certified && Normalize(e.Status) == EnvelopeActive
}

// HoldsScope reports whether the envelope grants scope. No scope asked for is
// always granted; scopes match the same case- and punctuation-insensitive way
// every cross-reference in the stack does.
func (e *CapabilityEnvelope) HoldsScope(scope any) bool {
	if scope == nil || scope == "" {
		return true
	}
	want := Normalize(fmt.Sprint(scope))
	for _, held := range e.Scopes {
		if Normalize(held) == want {
			return true
		}
	}
	return false
}

// WithinTier reports whether the requested autonomy tier is within the
// envelope's maximum. An unreadable tier is treated as out of bounds -- a
// request whose authority we cannot establish is not authorized.
func (e *CapabilityEnvelope) WithinTier(tier any) bool {
	if tier == nil || tier == "" {
		return true
	}
	n, ok := asInt(tier)
	if !ok {
		return false
	}
	return n <= e.MaxAutonomyTier
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	}
	return 0, false
}

// signaturePayload is the canonical bytes the signature covers: the
// authority-bearing fields only, so any tampering with certification, scope,
// tier or standing breaks the signature. Trust score and the signature itself
// are excluded -- trust changes are not authority grants, and a field cannot
// sign itself.
func (e *CapabilityEnvelope) signaturePayload() []byte {
	scopes := make([]string, len(e.Scopes))
	for i, s := range e.Scopes {
		scopes[i] = Normalize(s)
	}
	sort.Strings(scopes)
	fields := map[string]any{
		"agent":             e.Agent,
		"certified":         e.Certified,
		"scopes":            scopes,
		"max_autonomy_tier": e.MaxAutonomyTier,
		"status":            Normalize(e.Status),
		"issued_at":         e.IssuedAt,
	}
	// json.Marshal sorts map keys, so the payload is canonical.
	b, _ := json.Marshal(fields)
	return b
}

// ComputeSignature is a content signature over the authority fields. With a
// secret it is an HMAC-SHA256 -- unforgeable without the key, and not
// vulnerable to the length-extension a plain hash of secret+payload would be.
// Without a secret it is a bare SHA-256 tamper-evidence checksum.
func (e *CapabilityEnvelope) ComputeSignature(secret string) string {
	payload := e.signaturePayload()
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		return fmt.Sprintf("%x", mac.Sum(nil))
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum[:])
}

// Sign stamps the envelope with its current signature.
func (e *CapabilityEnvelope) Sign(secret string) *CapabilityEnvelope {
	e.Signature = e.ComputeSignature(secret)
	return e
}

// SignatureValid reports whether the stored signature matches the current
// authority fields, compared in constant time so verification leaks nothing
// about the secret. A record edited on disk -- a status flipped back to active
// by hand -- no longer verifies.
func (e *CapabilityEnvelope) SignatureValid(secret string) bool {
	if e.Signature == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(e.Signature), []byte(e.ComputeSignature(secret))) == 1
}

// Floor is the absolute, non-waivable authority floor: whether this envelope
// exists as a certified, un-withdrawn, untampered credential at all. No
// reasoning may override this -- a revoked envelope authorizes nothing, and
// the model is never asked to reconsider that.
func (e *CapabilityEnvelope) Floor(secret string) (bool, string) {
	if !e.Certified {
		return false, fmt.Sprintf("agent %q holds no certified envelope", e.Agent)
	}
	switch Normalize(e.Status) {
	case EnvelopeRevoked:
		return false, fmt.Sprintf("agent %q envelope is revoked", e.Agent)
	case EnvelopeSuspended:
		return false, fmt.Sprintf("agent %q envelope is suspended", e.Agent)
	}
	if e.Signature != "" && !e.SignatureValid(secret) {
		return false, fmt.Sprintf("agent %q envelope signature does not verify -- record tampered", e.Agent)
	}
	return true, fmt.Sprintf("agent %q holds a live envelope (standing: %s)", e.Agent, Normalize(e.Status))
}

// Authorized is the full deterministic decision: the floor, then scope and
// tier. It is the offline fallback; when a model is bound the EnvelopePolicy
// judges scope and tier above the floor instead.
func (e *CapabilityEnvelope) Authorized(scope, tier any, secret string) (bool, string) {
	if ok, reason := e.Floor(secret); !ok {
		return false, reason
	}
	if !e.HoldsScope(scope) {
		return false, fmt.Sprintf("agent %q envelope does not hold scope %q", e.Agent, scope)
	}
	if !e.WithinTier(tier) {
		return false, fmt.Sprintf("agent %q envelope tier %d is below the required tier %v", e.Agent, e.MaxAutonomyTier, tier)
	}
	if Normalize(e.Status) == EnvelopeProbation {
		return true, fmt.Sprintf("agent %q is on probation -- authorized but flagged for review", e.Agent)
	}
	return true, fmt.Sprintf("agent %q holds an active, in-scope envelope", e.Agent)
}

// EnvelopeRegistry is the capability envelopes for an enterprise -- the
// authority model every gate consults. It is safe for concurrent use: the gate
// reads it from the concurrent govern fan-out while transitions may write it.
type EnvelopeRegistry struct {
	// Backend persists the registry as a JSON blob under StateName, through
	// any CatalogueBackend -- a file Store, a SQLBackend over any database, or
	// anything else. Nil keeps the registry in memory only.
	Backend   CatalogueBackend
	StateName string

	// SecretEnvVar names the environment variable holding the signing secret.
	// The secret is read by name only, never stored -- absent, signatures are
	// unkeyed checksums.
	SecretEnvVar string

	// Log, when set, records every state transition on the runtime's one audit
	// spine, so certification and revocation land beside the gate decisions
	// they drive.
	Log *ReasoningLog

	mu        sync.RWMutex
	envelopes map[string]*CapabilityEnvelope
}

// NewEnvelopeRegistry builds an empty registry.
func NewEnvelopeRegistry() *EnvelopeRegistry {
	return &EnvelopeRegistry{StateName: "authority_envelopes", envelopes: map[string]*CapabilityEnvelope{}}
}

// Secret is the signing secret, read from its environment variable by name.
func (r *EnvelopeRegistry) Secret() string {
	if r.SecretEnvVar == "" {
		return ""
	}
	return os.Getenv(r.SecretEnvVar)
}

func (r *EnvelopeRegistry) ensure() {
	if r.envelopes == nil {
		r.envelopes = map[string]*CapabilityEnvelope{}
	}
	if r.StateName == "" {
		r.StateName = "authority_envelopes"
	}
}

// Load reads the registry from its backend, replacing the in-memory set. A
// registry with no backend, or whose state does not yet exist, loads empty.
func (r *EnvelopeRegistry) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	if r.Backend == nil {
		return nil
	}
	exists, err := r.Backend.Exists(r.StateName)
	if err != nil || !exists {
		return err
	}
	text, err := r.Backend.Read(r.StateName)
	if err != nil {
		return err
	}
	var payload struct {
		Envelopes []*CapabilityEnvelope `json:"envelopes"`
	}
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return fmt.Errorf("reading authority registry %q: %w", r.StateName, err)
	}
	r.envelopes = map[string]*CapabilityEnvelope{}
	for _, e := range payload.Envelopes {
		if e != nil && e.Agent != "" {
			r.envelopes[Normalize(e.Agent)] = e
		}
	}
	return nil
}

// Get returns the live envelope for agent, or nil.
func (r *EnvelopeRegistry) Get(agent string) *CapabilityEnvelope {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.envelopes[Normalize(agent)]
}

// Floor is the absolute authority floor for agent, live from the registry:
// does a certified, un-withdrawn, untampered envelope exist at all. Never
// model-waivable -- this is what makes revocation immediate.
func (r *EnvelopeRegistry) Floor(agent string) (bool, string) {
	e := r.Get(agent)
	if e == nil {
		return false, fmt.Sprintf("agent %q holds no envelope -- uncertified actors may not act", agent)
	}
	return e.Floor(r.Secret())
}

// Authorized is the full deterministic decision for agent, consulting the live
// registry so a revocation between two cycles is enforced on the next one.
func (r *EnvelopeRegistry) Authorized(agent string, scope, tier any) (bool, string) {
	e := r.Get(agent)
	if e == nil {
		return false, fmt.Sprintf("agent %q holds no envelope -- uncertified actors may not act", agent)
	}
	return e.Authorized(scope, tier, r.Secret())
}

// -- state transitions --------------------------------------------------------

// Certify issues (or re-issues) an active, signed envelope -- the one
// transition that grants authority; everything else narrows or removes it.
func (r *EnvelopeRegistry) Certify(agent string, scopes []string, maxTier int, trust float64, issuedAt string) *CapabilityEnvelope {
	envelope := (&CapabilityEnvelope{
		Agent:           agent,
		Certified:       true,
		Scopes:          append([]string{}, scopes...),
		MaxAutonomyTier: maxTier,
		Status:          EnvelopeActive,
		TrustScore:      trust,
		IssuedAt:        issuedAt,
	}).Sign(r.Secret())

	r.mu.Lock()
	r.ensure()
	r.envelopes[Normalize(agent)] = envelope
	r.mu.Unlock()

	r.transition(agent, EnvelopeActive, "certified")
	return envelope
}

// SetTrust updates an agent's trust score, re-signing so the record stays
// coherent. It does not change standing.
func (r *EnvelopeRegistry) SetTrust(agent string, trust float64) error {
	return r.mutate(agent, func(e *CapabilityEnvelope) (string, string) {
		e.TrustScore = trust
		return e.Status, fmt.Sprintf("trust set to %g", trust)
	})
}

// Probation, Suspend, Revoke and Reinstate move an agent's standing. Each
// re-signs so the signature always covers the current standing -- a revoked
// envelope whose signature still said active would be a hole.
func (r *EnvelopeRegistry) Probation(agent, reason string) error {
	return r.setStatus(agent, EnvelopeProbation, orDefault(reason, "placed on probation"))
}
func (r *EnvelopeRegistry) Suspend(agent, reason string) error {
	return r.setStatus(agent, EnvelopeSuspended, orDefault(reason, "suspended"))
}
func (r *EnvelopeRegistry) Revoke(agent, reason string) error {
	return r.setStatus(agent, EnvelopeRevoked, orDefault(reason, "revoked"))
}
func (r *EnvelopeRegistry) Reinstate(agent, reason string) error {
	return r.setStatus(agent, EnvelopeActive, orDefault(reason, "reinstated"))
}

func (r *EnvelopeRegistry) setStatus(agent, status, reason string) error {
	return r.mutate(agent, func(e *CapabilityEnvelope) (string, string) {
		e.Status = status
		return status, reason
	})
}

// mutate applies change under the write lock, re-signs, persists and records.
func (r *EnvelopeRegistry) mutate(agent string, change func(*CapabilityEnvelope) (status, reason string)) error {
	r.mu.Lock()
	r.ensure()
	e := r.envelopes[Normalize(agent)]
	if e == nil {
		r.mu.Unlock()
		return fmt.Errorf("no envelope for agent %q -- certify it first", agent)
	}
	status, reason := change(e)
	e.Sign(r.Secret())
	r.mu.Unlock()

	r.transition(agent, status, reason)
	return nil
}

// transition persists the registry and records the change on the audit spine.
func (r *EnvelopeRegistry) transition(agent, status, reason string) {
	if err := r.Persist(); err != nil && r.Log != nil {
		r.Log.Record(Record{
			Stage:  "certification",
			Inputs: map[string]any{"agent": agent, "status": status},
			Output: "not persisted: " + err.Error(),
		})
	}
	if r.Log != nil {
		r.Log.Record(Record{
			Stage:     "certification",
			Inputs:    map[string]any{"agent": agent, "status": status},
			Output:    fmt.Sprintf("%s -> %s", agent, status),
			Rationale: reason,
		})
	}
}

// Persist writes the registry to its backend as JSON, so a transition survives
// the process. A no-op when no backend is configured.
func (r *EnvelopeRegistry) Persist() error {
	r.mu.RLock()
	backend, name := r.Backend, r.StateName
	envelopes := make([]*CapabilityEnvelope, 0, len(r.envelopes))
	// Persist in a stable order so the stored blob does not churn.
	names := make([]string, 0, len(r.envelopes))
	for key := range r.envelopes {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		envelopes = append(envelopes, r.envelopes[key])
	}
	r.mu.RUnlock()

	if backend == nil {
		return nil
	}
	blob, err := json.MarshalIndent(map[string]any{"envelopes": envelopes}, "", "  ")
	if err != nil {
		return err
	}
	return backend.Write(name, string(blob)+"\n")
}

func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// -- enforcement --------------------------------------------------------------

// EnforceEnvelopes attaches envelope enforcement to a runtime at runtime scope.
// Every subsequent cycle whose intent names an acting agent must clear the
// agent's live envelope before anything runs -- the single choke point now
// covering *who* may act as well as *whether* the action is allowed.
//
// A human-initiated intent (no agent named in the context) is not applicable,
// the same off-unless-declared posture as Claim and Tenant. The registry's
// audit log is bound to the runtime's, so certification and revocation land on
// the same spine as the gate decisions they drive.
func EnforceEnvelopes(runtime *Runtime, registry *EnvelopeRegistry, name string) *Policy {
	if name == "" {
		name = "Capability Envelope"
	}
	if registry.Log == nil {
		registry.Log = runtime.ReasoningLog
	}
	policy := &Policy{
		Name: name,
		Statement: "The acting agent's capability envelope must authorize this action. Given the " +
			"scopes the envelope grants (envelope_scopes) and its maximum autonomy tier " +
			"(envelope_max_autonomy_tier), judge whether it covers the requested scope " +
			"(requested_scope) and tier (requested_tier). The envelope has already cleared the " +
			"certification and revocation floor; decide only whether the granted authority reaches " +
			"this particular action.",
		// The deterministic offline decision, computed in the gate and injected
		// as envelope_authorizes, is the fallback when no model is bound.
		FallbackExpression: "envelope_authorizes",
		Gate:               envelopeGate(registry),
	}
	runtime.AddPolicy(policy)
	return policy
}

// envelopeGate is the reason-first gate: the non-waivable floor, decided here
// so the model never sees a withdrawn credential, then scope and tier
// delegated to the runtime's judge over an enriched copy of the context.
func envelopeGate(registry *EnvelopeRegistry) PolicyGate {
	return func(ctx context.Context, base PolicyJudge, policy *Policy, context map[string]any) (bool, string, error) {
		agent := firstString(context, actorKeys)
		if agent == "" {
			return true, "no acting agent in context -- envelope policy not applicable (human-initiated)", nil
		}

		// The floor is absolute: uncertified, revoked, suspended or tampered is
		// decided here and never reaches the model.
		if ok, reason := registry.Floor(agent); !ok {
			return false, reason, nil
		}

		envelope := registry.Get(agent)
		scope := firstValue(context, scopeKeys)
		tier := firstValue(context, tierKeys)
		detOK, detReason := envelope.Authorized(scope, tier, registry.Secret())

		// Enrich a COPY -- the shared context is read concurrently by every
		// other policy's judge, so it must not be mutated here.
		enriched := make(map[string]any, len(context)+7)
		for k, v := range context {
			enriched[k] = v
		}
		enriched["acting_agent"] = agent
		enriched["envelope_scopes"] = strings.Join(envelope.Scopes, ", ")
		enriched["envelope_max_autonomy_tier"] = envelope.MaxAutonomyTier
		enriched["envelope_standing"] = envelope.Status
		enriched["envelope_trust_score"] = envelope.TrustScore
		enriched["requested_scope"] = scope
		enriched["requested_tier"] = tier
		enriched["envelope_authorizes"] = detOK

		// Delegate scope/tier to the runtime's judge: a bound model reasons over
		// the statement and the enriched facts; offline it evaluates the
		// fallback expression envelope_authorizes. The floor above is what the
		// model is never allowed to override.
		complies, rationale, err := base.Judge(ctx, policy, enriched)
		if err != nil {
			return false, rationale, err
		}
		if rationale == "" {
			rationale = detReason
		}
		return complies, rationale, nil
	}
}
