# EAR → Go porting tracker

Living status of the Go port (`go/`) against the Python package (`ear/`, ~88
modules, ~21.5k lines). Update the marks as work lands.

**Legend**

| Mark | Meaning |
| --- | --- |
| ✅ | Done — behaviour-complete for the ported scope, tested |
| 🟡 | Partial — deterministic path done; an LLM path or sub-feature pending |
| 🟣 | Ported-but-dormant — engine/signature exists, not wired into the cycle |
| 🔵 | Reconceived — intentionally different design from Python (not a gap) |
| ⬜ | Not started |

**Progress summary**

| Area | ✅ | 🟡 | 🟣 | 🔵 | ⬜ |
| --- | --- | --- | --- | --- | --- |
| Core data model & spine | 14 | 3 | 0 | 0 | 0 |
| Pipeline stages | 13 | 1 | 0 | 0 | 1 |
| DSPy layer (engine/LM) | 3 | 2 | 1 | 1 | 0 |
| Strategy / loader | 2 | 1 | 0 | 0 | 0 |
| Go-idiom enhancements | 6 | 0 | 0 | 0 | 0 |
| Category B (infra/AGI planes) | 1 | 0 | 0 | 1 | ~40 |

---

## 1. Core data model & spine

- ✅ `intent` — Intent, markdown round-trip
- ✅ `skill` — Skill, instruction fallback, markdown
- ✅ `persona` — Persona, skill stacking
- ✅ `step` — Step, delegation
- 🟡 `workflow` — steps/policies/contract/RetryBudget ✅; `Pattern:`/`Routes:` parsed but **inert** (need Panel/Journey)
- ✅ `process` — Process, workflow stacking
- ✅ `tool` — Tool data model + describe
- ✅ `contract` — structural `Judge` + `_formalize` skip (no model) ✅; **LLM field extraction + `JudgeContractConformance` + hinted retry wired** (`contract.go`, formalize stage)
- ✅ `policy` — fallback-expr + LLM judge + approval gates + approvers + escalation-days
- ✅ `tenant` — Tenant, fiscal-year bounds
- ✅ `section` — parser, Coerce/Normalize/Quote, `argumentBlocks`
- ✅ `safe_evaluator` — `safeeval.go`, tokenizer + recursive-descent, no eval
- ✅ `evidence` — Evidence
- ✅ `memory` — layers + context window ✅; **LLM summarizer wired** via `Memory.Summarizer` (`SummarizeHistory`, digest fallback)
- ✅ `experience` — Experience aggregation
- 🟡 `adaptation` — deterministic most-common ✅; **LLM distill dormant** (`DistillInsight`)
- ✅ `adapter` — as `Runtime.adapt` + `AdaptEvery`
- 🟡 `reasoning_log` — records + JSONL sink + iterator + retention + **token accounting** ✅; **usage-report markdown + dollar costing ⬜**

## 2. Pipeline stages

A **composable `[]Stage` pipeline** over a shared `*Cycle` (see `cycle.go`,
`stages.go`) — reorderable/insertable/removable, not a hardcoded straight
line. Each judged stage branches deterministic-or-LLM on `Runtime.LM`, so
binding a model lights up the ported signatures with no pipeline change.

- ✅ `governor` — concurrent, seam-judged (`PolicyJudge`), fail-closed
- ✅ `discoverer` — keyword ✅ + `DiscoverRelevantProcesses` wired
- ✅ `selector` — dedupe ✅ + `SelectProcesses` wired (>1 candidate)
- ✅ `composer` — flatten
- ✅ `scheduler` — composition order ✅ + `ScheduleWorkflows` wired (>1 workflow)
- ✅ `delegator` — authored-only ✅ + `DelegateSteps` wired
- ✅ `deliberator`/`decider`/`executor`/`performer`/`orchestrator`/`initializer` — collapsed into the pipeline
- ✅ `recaller` — full-window ✅ + `RecallRelevantMemory` wired
- ✅ `explainer` — f-string ✅ + `ExplainDecision` wired
- ✅ `auditor` — audited flag ✅ + `AuditEvidence` wired
- ✅ `learner` — observe into Experience
- ✅ `validator` — empty-decision guard
- 🟡 `reasoner` — deterministic + `LMReasoner`/`ReasonAboutIntent` ✅; **tool-use loop + progressive skill selection dormant**
- ⬜ `librarian` — needs `knowledge` (not ported)

## 3. DSPy layer (EAR's native structured prompting)

- ✅ `judgment` — `judgment.go`: Field/Kind/Judgment, render, parse, Prediction, cache boundary
- 🟡 `signatures` — 13 typed `Signature[In,Out]` ported; **10 wired**
  (policy, reason, discover, select, schedule, delegate, recall, explain,
  audit, contract-conformance, summarize); **2 dormant** (rank-skills,
  distill); ~20 more Python signatures not yet ported
- ✅ `llm` — `lm.go`+`llm_client.go`: LM interface, ScriptedLM, HTTPClient (Anthropic + OpenAI-compatible), retries, cache-prefix, usage/`CallHistory`
- 🟣 `skill_selector` — `RankRelevantSkills` ported, not wired
- 🔵 `model_binding` — reconceived as `Reasoner`/`PolicyJudge` seams + `Runtime.LM`; **memory.md auto-binding of a model not wired** (explicit `WithLM` only)

**Seam wiring status:** the composable `[]Stage` pipeline wires govern, discover, select, schedule, delegate, recall, reason, explain and audit to the model when one is bound. Still deterministic-only: adaptation distill / progressive skill-selection.

## 4. Strategy / loader

- 🟡 `strategy` — history capacity, audit retention, tools, ontology, subagent limits, discovery guidance ✅; **model binding / MCP / knowledge / sandbox / energy / pricing / evolution / toolsets / auxiliary model / cross-session ⬜ (recognised, inert)**
- ✅ `ontology` — as part of Strategy
- ✅ `loader` — skills/personas/policies/workflows/contracts/processes/tenant/scopes + escalation + retries + strategy wiring

## 5. Go-idiom enhancements (net-new, no Python equivalent)

- ✅ `context.Context` threaded through the cycle (cancellation/deadlines)
- ✅ Concurrent governance (`parallelMap`, order-preserving, bounded)
- ✅ Functional options (`WithReasoner`/`WithPolicyJudge`/`WithLM`/`WithMemoryCapacity`/…)
- ✅ Generics (`resolve[T]`)
- ✅ JSONL sink + range-over-func record iterator
- ✅ ScriptedLM deterministic test double (LLM path runs in CI, no network)

## 6. Category B — whole modules not started

**Accounting/reporting:** ⬜ dollar costing (tokens × pricing) · ⬜ usage report

**Servers / UI / observability:** ⬜ `server` ⬜ `dashboard` ⬜ `monitor` ⬜ `web` ⬜ `mail`

**Distributed / infra / persistence:** ⬜ `kernel` ⬜ `k8s` ⬜ `sandbox` ⬜ `store` ⬜ `session_store` ⬜ `run` ⬜ `mcp_client` ⬜ `mcp_server` ⬜ `mcp_command_centre`

**Enterprise-AGI / governance / cognition planes:** ⬜ `enterprise` ⬜ `authority` ⬜ `compiler` ⬜ `journey` ⬜ `examiner` ⬜ `knowledge` ⬜ `knowledge_governance` ⬜ `evolution` ⬜ `evolution_loop` ⬜ `optimizer` ⬜ `acquirer` ⬜ `coder` ⬜ `epistemic` ⬜ `adversary` ⬜ `panel` ⬜ `goal` ⬜ `spawner` ⬜ `tool_binder` ⬜ `tools_cli` ⬜ `identity` ⬜ `task` ⬜ `exchange` ⬜ `thrift` ⬜ `carbon` ⬜ `energy` ⬜ `hardware` ⬜ `caveman` ⬜ `router`

**Reconceived / already covered:** 🔵 `parallel` → `parallelMap` · ✅ `approval` → `ApprovalVerdict`

---

## Recommended next order

1. ~~One `Stage` seam~~ ✅ **done** — the composable pipeline wires 9 of 13 signatures.
2. ~~Contract extraction~~ ✅ **done** — extract + `JudgeContractConformance` + hinted retry wired into the formalize stage; conformant data reaches the decision's evidence.
3. **LLM memory/adaptation** — wire `SummarizeHistory` + `DistillInsight`.
4. **Dollar costing** — parse `## Pricing`, multiply the now-tracked tokens.
5. **Tooling** — `tool_binder` + the reasoner tool-use loop.
6. Then category-B planes as needed (knowledge/librarian, session_store, MCP, server).

_Last reviewed: port through the composable `[]Stage` pipeline + typed generic signatures._
