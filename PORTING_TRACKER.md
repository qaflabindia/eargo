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
| Core data model & spine | 12 | 5 | 0 | 0 | 0 |
| Pipeline stages | 8 | 7 | 0 | 0 | 1 |
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
- 🟡 `contract` — structural `Judge` + `_formalize` skip record ✅; **LLM field extraction + meaning-level conformance not wired**
- ✅ `policy` — fallback-expr + LLM judge + approval gates + approvers + escalation-days
- ✅ `tenant` — Tenant, fiscal-year bounds
- ✅ `section` — parser, Coerce/Normalize/Quote, `argumentBlocks`
- ✅ `safe_evaluator` — `safeeval.go`, tokenizer + recursive-descent, no eval
- ✅ `evidence` — Evidence
- 🟡 `memory` — layers + compression + context window ✅; **LLM summarizer dormant** (`SummarizeHistory`)
- ✅ `experience` — Experience aggregation
- 🟡 `adaptation` — deterministic most-common ✅; **LLM distill dormant** (`DistillInsight`)
- ✅ `adapter` — as `Runtime.adapt` + `AdaptEvery`
- 🟡 `reasoning_log` — records + JSONL sink + iterator + retention + **token accounting** ✅; **usage-report markdown + dollar costing ⬜**

## 2. Pipeline stages

Collapsed from Python's one-object-per-stage into Runtime methods + two seams.

- ✅ `governor` — concurrent, seam-judged (`PolicyJudge`), fail-closed
- 🟡 `discoverer` — keyword ✅; 🟣 `DiscoverRelevantProcesses` dormant
- 🟡 `selector` — dedupe ✅; 🟣 `SelectProcesses` dormant
- ✅ `composer` — flatten
- 🟡 `scheduler` — composition order ✅; 🟣 `ScheduleWorkflows` dormant
- 🟡 `delegator` — authored-only ✅; 🟣 `DelegateSteps` dormant
- ✅ `deliberator`/`decider`/`executor`/`performer`/`orchestrator`/`initializer` — collapsed into `Reason`
- 🟡 `recaller` — full-window ✅; 🟣 `RecallRelevantMemory` dormant
- 🟡 `explainer` — f-string ✅; 🟣 `ExplainDecision` dormant
- 🟡 `auditor` — audited flag ✅; 🟣 `AuditEvidence` dormant
- ✅ `learner` — observe into Experience
- ✅ `validator` — empty-decision guard
- 🟡 `reasoner` — deterministic + `LMReasoner`/`ReasonAboutIntent` ✅; **tool-use loop + progressive skill selection dormant**
- ⬜ `librarian` — needs `knowledge` (not ported)

## 3. DSPy layer (EAR's native structured prompting)

- ✅ `judgment` — `judgment.go`: Field/Kind/Judgment, render, parse, Prediction, cache boundary
- 🟡 `signatures` — 13 ported; **2 wired** (`JudgePolicyCompliance`, `ReasonAboutIntent`), **11 dormant**; ~20 more Python signatures not yet ported
- ✅ `llm` — `lm.go`+`llm_client.go`: LM interface, ScriptedLM, HTTPClient (Anthropic + OpenAI-compatible), retries, cache-prefix, usage/`CallHistory`
- 🟣 `skill_selector` — `RankRelevantSkills` ported, not wired
- 🔵 `model_binding` — reconceived as `Reasoner`/`PolicyJudge` seams + `Runtime.LM`; **memory.md auto-binding of a model not wired** (explicit `WithLM` only)

**Seam wiring status:** `Reasoner` ✅ · `PolicyJudge` ✅ · discovery/selection/scheduling/delegation/recall/audit/explain/summarize/distill ⬜ (no seam yet)

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

1. **One `Stage` seam** → lights up discovery, selection, scheduling, delegation, recall, audit, explain at once (11 dormant signatures → live).
2. **Contract extraction** — wire `contract.extract` + `JudgeContractConformance` into `formalize` (the one correctness-relevant gap).
3. **LLM memory/adaptation** — wire `SummarizeHistory` + `DistillInsight`.
4. **Dollar costing** — parse `## Pricing`, multiply the now-tracked tokens.
5. **Tooling** — `tool_binder` + the reasoner tool-use loop.
6. Then category-B planes as needed (knowledge/librarian, session_store, MCP, server).

_Last reviewed: port through commit `7a1414e` (token/usage accounting)._
