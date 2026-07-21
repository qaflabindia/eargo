# EAR (Go) — a Go port of the Enterprise Agentic Runtime

This is a Go port of [EAR](https://github.com/qaflabindia/EAR), the
Enterprise Agentic Runtime. It keeps EAR's authoring model — prompts stacked into skills,
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

## Idiomatic to Go, not transliterated from Python

This is a redesign around Go's strengths, not a line-for-line port:

- **`context.Context` threads the whole cycle.** `Reason(ctx, intent,
  approval)` honours cancellation and deadlines at every checkpoint — the
  defining idiom for a runtime whose real stages are network calls. A
  cancelled context aborts the cycle and returns `ctx.Err()`.
- **The two real seams are interfaces, not 14 empty structs.** EAR calls its
  judgment stages "seams" — swap one implementation for another. In Go that
  is `PolicyJudge` (how a policy is judged) and `Reasoner` (how the runtime
  deliberates), each with a deterministic default and a provider-backed
  implementation slotting in untouched. The mechanical steps are plain
  methods. No per-stage allocation, no indirection tax.
- **Governance fans out concurrently.** Policies are judged in parallel
  (`parallelMap`, order-preserving, bounded to `GOMAXPROCS`, dependency-free)
  and folded back in order so the audit trail stays deterministic. When the
  judge is an LLM, this turns a *sum* of latencies into a *max* — a serial
  ~400 ms of ten provider calls becomes one round-trip's wait. A judge error
  fails the cycle **closed** rather than passing governance silently.
- **Functional options** (`NewRuntime(name, WithReasoner(...),
  WithPolicyJudge(...), WithMemoryCapacity(...))`) keep construction stable
  as configuration grows.
- **Generics** collapse the loader's four near-identical reference resolvers
  into one `resolve[T]`.
- **The audit trail streams and iterates.** Set `ReasoningLog.Sink` to any
  `io.Writer` for append-only JSONL (the trail Python flushes to disk), and
  walk records with a `range`-over-func iterator (`for rec := range
  log.Records()`, Go 1.23+) without materializing a slice.

Everything is race-clean (`go test -race`) and benchmarked
(`BenchmarkReasonDeterministic`, `BenchmarkSafeEval`).

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
| Audit trail (`ReasoningLog`, JSONL sink, record iterator) | `reasoninglog.go` | ✅ |
| Seam interfaces + defaults (`PolicyJudge`, `Reasoner`) | `stage.go` | ✅ |
| Order-preserving concurrent fan-out (`parallelMap`) | `concurrent.go` | ✅ |
| Per-cycle pipeline steps (govern → … → adapt) | `pipeline.go` | ✅ |
| Deterministic deliberation helpers | `reasoner.go` | ✅ |
| Runtime cycle (`Reason(ctx,…)`, two governance gates, evidence, memory, contract formalize, latency, retention) | `runtime.go` | ✅ |
| Operating strategy from memory.md (history capacity, audit retention, tools, ontology, subagent limits, discovery guidance) | `strategy.go` | ✅ |
| Functional options (`WithReasoner`, `WithPolicyJudge`, …) | `options.go` | ✅ |
| Markdown stack loader (`LoadRuntime`, generic `resolve[T]`, escalation-days + retries parsing) | `loader.go` | ✅ |
| Persisted audit trail (`TrailFile`: md/JSONL codecs, cross-session hash chain, `VerifyTrail`, `ReadTrail`) | `trailfile.go` | ✅ |
| CLI application (`run`/`repl`/`inspect`/`trail`/`usage`/`verify`/`demo`, governed exit codes) | `cmd/ear` | ✅ |

### Deterministic behaviours completed from the "partially ported" gaps

These were data-model-present/behaviour-missing and needed no LLM, so they
are now done, matching the Python package's no-model path:

- **Policy escalation** — `Escalate: after N days` is parsed into
  `EscalationDays`; an unreadable period fails the load loudly.
- **Workflow retries** — `Retries: … twice` is parsed into `RetryBudget`
  (digit or spoken count); an unreadable count fails the load.
- **Contract conformance** — `Contract.Judge` does the structural check
  (every declared field present and non-empty), and the runtime records the
  contract **skip** on the trail when no model is bound to extract it.
- **Audit trail accounting** — every cycle records a `usage` step with
  wall-clock latency (including blocked/parked cycles), and a declared
  `keep N days` retention window rotates expired cycles off the trail. When
  an `LM` is bound (via `WithLM`), the `usage` step reports the cycle's real
  model calls, prompt/completion tokens, cache tokens and retries, read from
  the LM's own call history (the `CallHistory` seam).
- **memory.md Strategy** — parsed and wired: context-history capacity, audit
  retention, declared tools, working ontology, subagent limits, and
  skills-discovery guidance.

Still LLM-gated within those modules (need a live model): contract field
*extraction* and meaning-level conformance, the tool-use loop, and
memory/adaptation LLM summarisation. Dollar costing (tokens × declared
pricing) is the small remaining accounting step on top of the now-wired
token usage.

### The DSPy layer — native structured prompting (now ported)

EAR refuses to depend on DSPy, LiteLLM or any provider SDK; it ships its own
replacement, and this port carries it:

- **`judgment.go`** — the engine. A `Judgment` is a declared reasoning task
  (an instruction + typed input/output `Field`s). It renders a prompt whose
  answer is markdown `## Heading` sections — the exact codec EAR already
  parses — and reads the reply back into a typed `Prediction`
  (`pred.Bool("complies")`, `pred.List("names")`, `pred.Map("args")`). A
  missing field degrades to a safe zero value; kinds are text/str/bool/list/map;
  a `CacheBoundary` input yields a provider-neutral cache prefix.
- **`signatures.go`** — the catalogue of concrete Judgments
  (`JudgePolicyCompliance`, `ReasonAboutIntent`, `DiscoverRelevantProcesses`,
  `JudgeContractConformance`, …), faithful to the Python instructions.
- **`lm.go` / `llm_client.go`** — the `LM` interface, a deterministic
  `ScriptedLM` test double, and `HTTPClient`, a dependency-free client
  (`net/http` + `encoding/json`) speaking Anthropic's Messages API and any
  OpenAI-compatible endpoint, with retries and a cache-prefix hint.
- **`seams.go`** — `LMReasoner` and `LMJudge` plug the engine into the
  runtime's two seams. `NewRuntime(name, WithLM(client))` makes the runtime
  reason and judge policies against the model; with no LM it stays on the
  deterministic defaults.

```go
lm := ear.NewHTTPClient("anthropic", "claude-opus-4-8", "ANTHROPIC_API_KEY", "")
rt := ear.NewRuntime("Credit Risk Runtime", ear.WithLM(lm))
// policies with a plain-English Statement are now judged in natural language;
// deliberation runs the ReasonAboutIntent signature.
```

The whole layer is tested end-to-end against `ScriptedLM` (no network), so
the LLM path is exercised deterministically in CI.

**Not yet ported** (infrastructure surfaces): the HTTP servers and
dashboard, MCP client/server, the sandbox, Postgres/k8s backends, the
optimizer/evolution/acquirer planes, and the knowledge/BM25 retriever. The
LLM-facing pipeline stages beyond the two runtime seams (discovery,
selection, scheduling, delegation) have their signatures ported but are not
yet wired to run against the model — the deterministic paths still apply.

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

decision, err := rt.Reason(context.Background(), ear.NewIntent(
    "Underwrite a $20,000 consumer loan application",
    map[string]any{"loan_amount": 20000.0, "debt_to_income": 0.28},
), nil)
```

To reason against a live model, swap the seams — the pipeline is untouched:

```go
rt := ear.NewRuntime("Credit Risk Runtime",
    ear.WithReasoner(myLLMReasoner),   // implements ear.Reasoner
    ear.WithPolicyJudge(myLLMJudge),   // implements ear.PolicyJudge
)
```

Or author the whole stack in markdown and load it — the same
`examples/credit_risk_stack` the Python package ships:

```go
rt, err := ear.LoadRuntime("examples/credit_risk_stack", "")
decision, err := rt.Reason(context.Background(), intent, nil)
```

## CLI

`cmd/ear` is a complete application over the library, not a demo:

```sh
go build -o ear ./cmd/ear

./ear run examples/credit_risk_stack \
    "Underwrite a $20,000 consumer loan application" \
    loan_amount=20000 debt_to_income=0.28 credit_score=742
./ear run <stack> "<intent>" [k=v ...] -approve -approver riya   # answer a parked gate
./ear repl <stack>          # interactive session; memory persists across restarts
./ear inspect <stack>       # how the markdown was assembled, before reasoning
./ear trail <stack>         # the persisted reasoning trail, readable
./ear usage <stack>         # the usage ledger from a persisted JSONL trail
./ear verify <stack>        # prove the trail's hash chain unbroken
./ear kernel <stack>...     # run stacks as a persistent scheduled runtime
./ear serve <stack>...      # the same, behind an HTTP control plane
./ear demo                  # zero-setup built-in stack
```

Exit codes are governed outcomes, so scripts can branch without parsing
prose: `0` decided, `1` blocked by policy (or a broken trail chain), `2`
error, `3` parked for human approval.

The trail declared in memory.md's `## Reasoning Audit Trail` persists to
disk (markdown or JSONL by extension), hash-chained across sessions and
tamper-evident: `ear verify` names the exact record where an altered trail
first breaks.

## The kernel: a runtime the enterprise operates

`ear run` reasons once and exits. `ear kernel` is the same runtime made
persistent — the difference between a library you call and something an
enterprise operates.

The Kernel is a scheduler: a **process table** of named `Runtime` instances,
each with its own memory, tenant and hash-chained trail, and a **run queue**
of tasks over them. Its loop is a kernel's idle loop — while there is work,
dispatch it; otherwise sleep until an interrupt. The interrupt line is a
buffered channel and the timer a `time.Timer`, so a stack that runs hourly
costs nothing for the other fifty-nine minutes.

**Nothing in the kernel reasons.** It decides only *when* work runs; the
judgment stays in the instances, and dispatch goes through their normal
cycle — so policies still gate it, the tenant boundary still refuses it, the
trail still records it and the session store still persists it. That
separation is what makes it safe for this to be the component that never
stops.

**Standing work is authored, not coded.** The same discipline already used
for the budget, the pricing and the model binding:

```markdown
## Scheduled Work

- Every 15 minutes, reason "Sweep the overnight application queue."
- Every 24 hours, reason "Produce the daily underwriting summary."
```

```sh
./ear kernel stacks/underwriting stacks/collections -workers 2
./ear kernel stacks/underwriting -once            # run the schedule once and exit
./ear kernel stacks/underwriting -subject svc:nightly -org acme
```

**Governance is not failure.** A violated policy, a parked approval gate, a
refused tenant boundary and a denied spawn all land `blocked`; only a
genuine fault lands `failed`, and one task's failure never takes the kernel
down. A panic in a seam is recovered at the dispatch boundary — in Go it
would otherwise reach the goroutine boundary and kill the process, which for
a control plane meant never to stop is the one unacceptable failure mode.

**Identity closes the boundary scheduled work needs.** A `Claim` carries a
caller's subject and the orgs they may act as; `Runtime.Reason` refuses a
foreign one before any stage runs, recording the attempt on the trail first.
Python threads the claim as a parameter through `reason()` and
`Kernel.submit()`; here it rides `context.Context`, already threaded through
every stage, seam, tool call and spawn — so the boundary travels with the
work and no existing signature changed. No claim supplied is not a
violation, the same off-unless-declared posture as `Tenant` itself.

**Fleet parallelism keeps one cycle per instance.** `-workers N` fans ready
work across *different* instances while serializing work *within* one, so
each instance stays the single writer of its own memory and hash-chained
trail. Both halves are tested: peak concurrency per instance never exceeds
one, and separate instances do genuinely overlap.

`Kernel.Dispatcher` is an execution seam — set it and each firing runs
wherever you send it (a remote executor, a job queue, a pod) while the
Kernel stays the single scheduler.

## The server: the control plane over the network

`ear kernel` needs shell access on the box. `ear serve` is the same Kernel
behind a small HTTP front door, so the enterprise can create instances,
submit work and observe the fleet from anywhere.

```sh
EAR_SERVER_TOKEN=… ./ear serve -addr :8080 -stacks ./stacks

GET    /health                        liveness, uptime, queue depth
GET    /kernel                        the process table and recent dispatches
GET    /instances                     what is registered
POST   /instances                     load a stack as a named instance
DELETE /instances/{name}              retire one
POST   /instances/{name}/submit       reason an intent, governed
POST   /instances/{name}/approve      release a parked approval gate
GET    /instances/{name}/status       org, processes, last intent and decision
GET    /instances/{name}/trail        recent records + hash-chain verification
```

**Solid by construction, not by afterthought.**

- **Auth.** A bearer token from `EAR_SERVER_TOKEN`, never hardcoded, compared
  in constant time. Every request is refused without it — including
  `/health`, so an unauthenticated caller learns nothing. Unset leaves the
  server open and it says so loudly on start: a development convenience you
  opt into, never a silent default.
- **Confinement.** Loading a stack is confined under `-stacks`; a path that
  escapes it is refused, symlinks resolved on both sides first. With no
  stacks root configured, loading from the wire is disabled outright — a
  server that loads any path a caller names is a remote file-read primitive.
- **The claim comes from the deployment, not the request.** A caller who can
  name their own org has no boundary at all, so `-subject`/`-org` set the
  identity all work runs under and a body that tries to assert one is
  ignored. There is a test that asserts exactly this.
- **Resilience.** Bodies are capped at 1 MiB, malformed JSON is a 400 rather
  than a crash, and every handler is wrapped so one bad request can never
  take the control plane down.
- **Governance is not an HTTP error.** A blocked policy or a parked gate
  returns `200` with `"outcome": "blocked"` or `"awaiting_approval"` and the
  policies named. The request succeeded; governance said no. Reserving 4xx
  for caller mistakes keeps the two genuinely distinguishable.
- **Approval without a shared filesystem.** The `approval.md` file-drop
  convention assumes the human and the runtime share a disk, which is untrue
  across a network. `POST /instances/{name}/approve` is the same release
  spoken over the wire, resubmitting the remembered intent with the verdict
  attached.

The routing is a pure function — `Handle(ctx, method, path, body) -> (status,
payload)` — so the whole API is tested without opening a socket, and the
socket layer holds no routing logic of its own.

## MCP: reaching out to what the author declared

MCP is an open JSON-RPC 2.0 protocol, and EAR speaks it directly — no SDK,
standard library only, because the protocol *is* the spec and the spec is
JSON over pipes.

Servers are declared in memory.md, never in code:

```markdown
## MCP

- credit_bureau: pulls credit reports and score history, via `bureau-mcp-server`
- core_banking: reads account balances and repayment history, via `corebank-mcp-server`
```

```go
client, err := runtime.ConnectMCP(ctx, "credit_bureau")
defer client.Close()
```

**The declaration is the authorization.** `ConnectMCP` refuses a name the
stack never declared, so connecting is the runtime reaching out to what the
author already named — never a capability appearing from nowhere. It is the
same discipline `BindTool` enforces for native tools, one level up: the
author declares the *server*, and the server's own catalogue supplies the
tools.

**A connected server's tools are ordinary bound tools**, namespaced by
server (`credit_bureau.lookup`) so two servers offering `search` never
shadow each other. They run through `InvokeTool` like any native tool:
judged by tool-scoped policies, recorded on the trail with arguments,
result and duration, and counted against the tool-loop budget. A tool that
reports failure returns to the model as text rather than crashing the cycle.

**Exactly one goroutine ever reads a server's stdout** — the pump `Connect`
starts, for the connection's lifetime. Each in-flight request registers a
one-slot channel keyed by its JSON-RPC id; anything nobody is waiting for
is dropped, since a response that outlived its caller's deadline is not an
error. That single persistent reader is what makes a client-side timeout
safe: nothing can spawn a second reader to race the pump and silently steal
a line meant for a later call. A server that hangs, dies, or answers with
malformed JSON fails as an `*McpError`, never silently.

The client is tested against a real subprocess speaking real JSON-RPC — the
test binary re-executes itself as an MCP server — covering the handshake,
listing, calling, tool-reported errors, timeouts, context cancellation,
non-JSON noise on stdout, and launch failure.

## Test

```sh
go test ./...
```

`loader_test.go` loads the real `examples/credit_risk_stack` markdown and
drives compliant, DTI-blocked, and loan-cap-blocked cycles through it, so
the parser, loader, policy wiring and pipeline are all exercised end to end.
