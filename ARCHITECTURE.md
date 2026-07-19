# EAR (Go) — Architecture

A fresh review of the Go port (`go/`, ~2,900 LOC + tests, one flat package
`ear`). Read alongside [`../docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md)
(the Python architecture this maps to) and
[`PORTING_TRACKER.md`](PORTING_TRACKER.md) (what is / isn't ported).

## Thesis

The Go edition is a **faithful port of EAR's deterministic spine plus its
DSPy-equivalent LLM layer, redesigned around Go's idioms** — not a
line-for-line transliteration. It keeps the three Python invariants
(markdown-native authoring, usable offline, dependency-free) and adds three
Go ones: **`context.Context` everywhere, static interfaces at the seams, and
concurrent governance**.

## Shape

- **One flat package `ear`** — no sub-packages; ~20 source files.
- **Exactly four interfaces**: `LM` and `CallHistory` (the model layer),
  `PolicyJudge` and `Reasoner` (the two runtime seams). Everything else is
  concrete structs and methods.
- **stdlib only** — `net/http`, `crypto/tls`, `encoding/json`, `sync`,
  `context`, `iter`. No third-party dependency, matching Python's ethos.

## The layered architecture (as built)

### Layer 1 — The stack (`model.go`, `policy.go`, `tenant.go`)
`Intent → Skill → Persona → Workflow → Process → Policy`, plus `Step`,
`Tool`, `Contract`. Plain structs with markdown round-tripping. Identical in
spirit to Python Layer 1.

### Layer 2 — The composable cycle (`cycle.go` + `stages.go` + `runtime.go`)
Python models ~18 stages as ~18 classes wired in a fixed `reason()`. Go makes
the cycle a **composable data pipeline**: a `[]Stage` executed over one shared
`*Cycle` value.

```go
type Stage interface { Name() string; Run(*Cycle) error }
type Cycle struct { Ctx context.Context; Runtime *Runtime; Intent Intent
                    Candidates []*Process; Plan []*Workflow; Decision any; ... }
```

`Runtime.Reason` is now a 6-line loop: `for _, s := range r.Pipeline { ctx
check; s.Run(c) }`. The pipeline (`defaultPipeline()`) is *data* — govern →
discover → select → compose → schedule → govern → delegate → recall → reason →
formalize → evidence → explain → audit → memory — so a caller can reorder,
insert, drop or swap stages. Each **judged** stage (discover, select,
schedule, delegate, recall, explain, audit) branches deterministic-or-LLM on
`Runtime.LM`, and the two headline seams stay interfaces:

```go
type Reasoner    interface { Reason(ctx, *Runtime, Intent, []*Workflow) (any, error) }
type PolicyJudge interface { Judge(ctx, *Policy, map[string]any) (bool, string, error) }
```

So binding a model (via `WithLM`) lights up 9 of the 13 signatures with no
pipeline change; offline, the same pipeline runs the deterministic paths.
`ctx` is checked before every stage; accounting + retention close in a
`defer`.

### Layer 3 — The LLM subsystem (`signature.go`, `judgment.go`, `signatures.go`, `lm.go`, `llm_client.go`, `seams.go`)
EAR's dependency-free DSPy replacement — redesigned around generics, not a
transliteration of the Python dynamic API:
- `signature.go` — **`Signature[In, Out]`**, a typed generic reasoning task.
  In/Out are structs whose `ear:"name,description"`-tagged fields declare the
  model's inputs and outputs; the field's **Go type drives parsing** (bool →
  yes/no, `[]string` → list, `map[string]string` → blocks, string → prose).
  `Run` returns a real typed `Out` — no `map[string]any`, no string keys, no
  casts. Reflection over struct tags with per-type field caching, the same
  pattern `encoding/json` uses.
- `judgment.go` — the dynamic engine underneath (`Judgment` + `Prediction`),
  kept for tasks whose fields are only known at runtime (contract extraction).
  `Signature` is the typed façade; `Judgment` is the reflective core.
- `signatures.go` — 13 concrete typed Signatures (of ~30 in Python).
- `lm.go` / `llm_client.go` — the `LM` interface, a `ScriptedLM` test double,
  and `HTTPClient` (Anthropic Messages + OpenAI-compatible, retries,
  cache-prefix, usage via `CallHistory`).
- `seams.go` — `LMReasoner` + `LMJudge` implement the two seams; `attachLM`
  binds a model across every seam (reason, judge, memory summarise, adaptation
  distil). `WithLM(lm)` is the programmatic path; a `## Model Selection`
  section in memory.md auto-binds at load (provider/model/params from prose,
  API key from the *named* env var, deterministic fallback when the key is
  absent) — so the model, like every other setting, is authored not coded.

### Layer 4 — Memory (`memory.go`)
`Evidence` / `Memory` / `Experience` / `Adaptation` — the four separated
layers, faithful. Compression and distillation use the deterministic path
(LLM summarise/distill are ported as signatures but not yet wired).

### Layer 5 — The audit spine (`reasoninglog.go`)
Present but **simplified** versus Python. Has: per-cycle records, an
`io.Writer` **JSONL sink**, a `range`-over-func **record iterator**
(`iter.Seq`), **cycle-level retention rotation**, and **per-cycle token/usage
accounting** read from the LM's `CallHistory`. Does **not** have Python's
SHA-256 **hash-chain / `verify()` / `resume()`** or the markdown usage
ledger — the tamper-evidence layer is the notable omission.

### Layer 6 — Strategy & loader (`strategy.go`, `loader.go`)
`LoadRuntime` stacks the markdown directory (skills/persona/workflow/process/
policy/tenant/memory) with a generic `resolve[T]`. `StrategyFromMarkdown`
parses the deterministic-relevant memory.md sections (history capacity, audit
retention, tools, ontology, subagent limits, discovery guidance); the model
binding / MCP / knowledge / sandbox / energy / pricing sections are
recognised but inert.

## Cross-cutting

- **Governance choke point** — `Runtime.govern` is the single gate,
  fanned out **concurrently** (see below), fail-closed on judge error. `Policy`
  is the universal primitive (statement judged by the seam, or safe-eval
  fallback; runtime- or workflow-scoped; approval gates park the cycle).
- **Boundary** — `Tenant` is present. `Claim`/identity is **not** ported.

## The Go idioms (what makes it "not just a port")

1. **`context.Context` threads the whole cycle** — cancellation/deadlines at
   every gate; a cancelled context aborts and returns `ctx.Err()`. Python has
   no equivalent.
2. **Concurrent governance** — `concurrent.go`'s `parallelMap` judges
   policies in parallel, bounded to `GOMAXPROCS`, order-preserving (each
   goroutine writes its own result index, no locks), folded back in order so
   the trail stays deterministic. Turns a *sum* of provider latencies into a
   *max*. Verified race-clean.
3. **Interfaces at the seams, not duck typing** — Python's `getattr`-based
   swapping becomes two static interfaces with compile-time contracts.
4. **Functional options** — `NewRuntime(name, WithLM(...), WithMemoryCapacity(...))`.
5. **Generics** — one `resolve[T]` replaces four near-identical resolvers.
6. **Streaming + iterators** — `io.Writer` JSONL sink and `iter.Seq` record iterator.

## Control flow of one cycle

```
Reason(ctx, intent, approval)
  ├─ defer: recordUsage(callsBefore) + applyRetention()
  └─ for stage in Pipeline:  (ctx checked before each)
       govern{Policy}       → parallelMap judge → enforce (block/park)
       discover · select · compose · schedule    (judged: keyword | signature)
       govern{Workflow}     → enforce
       delegate · recall                          (judged: authored | signature)
       reason               → seam: DefaultReasoner | LMReasoner
       formalize · evidence · explain · audit     (judged: f-string | signature)
       memory               → Record · Observe · adapt
  → c.Decision
```

## What's absent (vs Python)

Whole subsystems not ported (see the tracker for the full list): the
**execution substrate** (Kernel/Journey/Server/k8s/Sandbox/Spawner), the
**Enterprise-AGI overlay** (constitutions, authority envelopes, the three
planes, compiler), **self-improvement** (Optimizer/Evolution/Acquirer/Coder),
the **resource plane** (hardware/energy/carbon/thrift), **observability**
(dashboard/monitor), **persistence** (store/session_store), **knowledge**
(RAG/BM25), **identity/Claim**, **tool execution** (tool_binder), and
**router**.

## Assessment

**Strengths.** The port preserves EAR's coherence — one parser, one gate, one
audit spine, one memory taxonomy — while shedding the Python-isms that don't
belong in Go (empty stage-objects, duck typing). The two-seam design is the
right cut: it's the minimum surface that makes the runtime pluggable against
a live model, and it's proven end-to-end. Concurrency and cancellation are
genuine improvements the Python edition lacks. The whole LLM path runs in CI
against `ScriptedLM` with no network.

**Tensions / gaps.**
- **9 of 13 signatures wired** via the composable pipeline; the remaining
  four (progressive skill-selection, contract conformance, memory summarise,
  adaptation distil) stay deterministic — each is a stage-local branch away.
- **The audit spine lacks tamper-evidence** (no hash-chain/verify) — the one
  place the simplification weakens a security-relevant Python guarantee.
- **`any`-typed decisions** carry through the cycle (as in Python), trading
  static typing for fidelity to EAR's prose-decision model.
- Contract *extraction*, LLM memory summarisation, dollar costing, and the
  reasoner tool-loop remain unwired (all tracked).
