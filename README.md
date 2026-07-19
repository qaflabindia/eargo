# EAR (Go) — a Go port of the Enterprise Agentic Runtime

This directory is a Go port of [EAR](../README.md), the Enterprise Agentic
Runtime. It keeps EAR's authoring model — prompts stacked into skills,
skills into personas, personas into workflows, workflows into processes,
governed by policy, orchestrated by a runtime — and its plain-English,
markdown-native stack, while running natively on Go's standard library.

> **Why Go and not C?** The original request was a C rewrite. C would mean
> hand-rolling JSON, HTTP/1.1 and TLS just to talk to a provider, and
> modelling EAR's dynamic, reflective Python with structs and function
> pointers. Go's standard library gives `net/http` (client *and* server),
> `crypto/tls` and `encoding/json` for free — matching EAR's dependency-free
> ethos — and its structs + interfaces map cleanly onto the data model and
> the pipeline stages. So this is a Go port, by agreement.

## Scope of this port

The Python package is ~21,500 lines across 90+ modules. This port
implements EAR's **deterministic spine** — the part that runs identically
whether or not a model is bound, which in the Python package is exactly the
behaviour you get when no provider is configured:

| Area | Files | Status |
| --- | --- | --- |
| Shared markdown parser (`Section`/`Document`/`Body`, `Normalize`, `Coerce`, `Quote`) | `section.go` | ✅ |
| Safe expression evaluator (policy fallbacks; no `eval`) | `safeeval.go` | ✅ |
| Data model (`Intent`, `Skill`, `Persona`, `Step`, `Workflow`, `Process`, `Tool`, `Contract`) | `model.go` | ✅ |
| Governance (`Policy`, approval gates, approver allow-lists) | `policy.go` | ✅ |
| Memory layers (`Evidence`, `Memory`, `Experience`, `Adaptation`) | `memory.go` | ✅ |
| Tenancy (`Tenant`, fiscal-year bounds) | `tenant.go` | ✅ |
| Audit trail (`ReasoningLog`) | `reasoninglog.go` | ✅ |
| Per-cycle pipeline stages (Governor → … → Adapter) | `pipeline.go` | ✅ |
| Reasoner (deterministic decision + capabilities rendering) | `reasoner.go` | ✅ |
| Runtime cycle (`Reason`, two governance gates, evidence, memory) | `runtime.go` | ✅ |
| Markdown stack loader (`LoadRuntime`) | `loader.go` | ✅ |
| CLI demo | `cmd/ear` | ✅ |

**Not yet ported** (the LLM-facing and infrastructure surfaces): the
dependency-free HTTPS `LM` client and signatures, the HTTP servers and
dashboard, MCP client/server, the sandbox, Postgres/k8s backends, the
optimizer/evolution/acquirer planes, and the knowledge/BM25 retriever.
These sit on top of the spine and can be added incrementally; the spine is
what the rest is built on.

Every judgment stage that would call a live model in Python falls back here
to the same deterministic behaviour the Python package uses with no model
bound, so the runtime is fully usable and testable with no provider.

## Usage

```go
import ear "github.com/qaflabindia/ear"

// In code:
guru := &ear.Persona{Name: "Credit Risk Guru", Instructions: "Underwrite conservatively."}
guru.AddSkill(&ear.Skill{Name: "risk_grade", Prompt: "Combine the score tier and DTI band into a grade A-E."})

w := &ear.Workflow{Name: "Underwriting Workflow"}
w.AddStep("Band the credit profile and assign a risk grade.", guru)
w.AddPolicy(&ear.Policy{Name: "Loan Amount Cap", FallbackExpression: "loan_amount <= 75000"})

proc := &ear.Process{Name: "Underwriting"}
proc.AddWorkflow(w)

rt := ear.NewRuntime("Credit Risk Runtime")
rt.AddProcess(proc)

decision, err := rt.Reason(ear.NewIntent(
    "Underwrite a $20,000 consumer loan application",
    map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28},
), nil)
```

Or author the whole stack in markdown and load it — the same
`examples/credit_risk_stack` the Python package ships:

```go
rt, err := ear.LoadRuntime("examples/credit_risk_stack", "")
decision, err := rt.Reason(intent, nil)
```

## CLI

```sh
go run ./cmd/ear                     # built-in demo stack
go run ./cmd/ear ../examples/credit_risk_stack \
    "Underwrite a $20,000 consumer loan application" \
    loan_amount=20000 debt_to_income=0.28 credit_score=742
```

## Test

```sh
go test ./...
```

`loader_test.go` loads the real `examples/credit_risk_stack` markdown and
drives compliant, DTI-blocked, and loan-cap-blocked cycles through it, so
the parser, loader, policy wiring and pipeline are all exercised end to end.
