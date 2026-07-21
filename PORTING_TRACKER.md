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
| Core data model & spine | 16 | 1 | 0 | 0 | 0 |
| Pipeline stages | 15 | 0 | 0 | 0 | 0 |
| DSPy layer (engine/LM) | 5 | 1 | 0 | 0 | 0 |
| Strategy / loader | 2 | 1 | 0 | 0 | 0 |
| Go-idiom enhancements | 7 | 0 | 0 | 0 | 0 |
| Category B (infra/AGI planes) | 5 | 0 | 0 | 1 | ~37 |

---

## 1. Core data model & spine

- ✅ `intent` — Intent, markdown round-trip
- ✅ `skill` — Skill, instruction fallback, markdown
- ✅ `persona` — Persona, skill stacking
- ✅ `step` — Step, delegation
- 🟡 `workflow` — steps/policies/contract/RetryBudget ✅; **`Pattern:` wired** (convenes a `Panel` when the scheduled plan carries a pattern and ≥2 personas); `Routes:` parsed but inert (needs Journey)
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
- ✅ `adaptation` — deterministic most-common ✅; **LLM distill wired** via `AdaptationBank.Distiller` (`DistillInsight`, most-common fallback)
- ✅ `adapter` — as `Runtime.adapt` + `AdaptEvery`
- ✅ `reasoning_log` — records + JSONL sink + iterator + retention + token accounting + **dollar costing + usage-report ledger + hash-chain/verify** + **persisted `TrailFile`** (md/JSONL codec by extension, cycle numbering + chain resumed across sessions, `VerifyTrail` names the exact tampered record, `ReadTrail` lossless JSONL read-back) ✅

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
- ✅ `reasoner` — deterministic + `LMReasoner`/`ReasonAboutIntent` + progressive skill selection + **native tool-use loop** (`ChooseToolAction`, recovery discipline) ✅
- ✅ `librarian` — BM25 retrieval + **LLM relevance judging** (`SelectRelevantPassages`) + RAG augmentation + citations, all wired

## 3. DSPy layer (EAR's native structured prompting)

- ✅ `judgment` — `judgment.go`: Field/Kind/Judgment, render, parse, Prediction, cache boundary
- ✅ `signatures` — typed `Signature[In,Out]` catalogue, all wired (13 core + ChooseToolAction/SelectRelevantPassages/GistPassage + the four panel signatures: ChooseNextSpeaker/SpeakInPanel/SpeakOrUseTool/SynthesizePanel)
  (policy, reason, discover, select, schedule, delegate, recall, explain,
  audit, contract-conformance, summarize, distill, rank-skills); ~20 more
  Python signatures not yet ported
- ✅ `llm` — `lm.go`+`llm_client.go`: LM interface, ScriptedLM, HTTPClient (Anthropic + OpenAI-compatible), retries, cache-prefix, usage/`CallHistory`
- ✅ `skill_selector` — `RankRelevantSkills` wired: progressive per-persona skill selection in the LM reasoner (>1-skill guard, all-skills fallback)
- ✅ `model_binding` — reconceived as `Reasoner`/`PolicyJudge` seams + `Runtime.LM`; **memory.md `## Model Selection` auto-binds at load** (provider/model/params from prose, key from the named env var, deterministic fallback when absent); `WithLM` the programmatic path

**Seam wiring status:** all 13 ported signatures are wired — the composable `[]Stage` pipeline (govern, discover, select, schedule, delegate, recall, reason, explain, audit), contract extraction/conformance, memory summarise, adaptation distil, and progressive skill selection all run against the model when one is bound.

## 4. Strategy / loader

- 🟡 `strategy` — history capacity, audit retention, tools, ontology, discovery guidance ✅; **audit path wired** (`## Reasoning Audit Trail` → persisted `TrailFile` at load); **model binding wired** (`## Model Selection` auto-binds at load, key from env); **auxiliary model wired** (`## Auxiliary Model` backs memory compression + adaptation distillation, same parse rule, own fields/env var); **cross-session store wired** (`## Cross-Session Data` path parsed, restore-before/save-after); **subagent spawning wired** (`## Subagent Spawning` enable/limit → `Spawner`); MCP / sandbox / energy / evolution / toolsets ⬜ (recognised, inert — deployment-only for a library target)
- ✅ `ontology` — as part of Strategy
- ✅ `loader` — skills/personas/policies/workflows/contracts/processes/tenant/scopes + escalation + retries + strategy wiring

## 5. Go-idiom enhancements (net-new, no Python equivalent)

- ✅ `context.Context` threaded through the cycle (cancellation/deadlines)
- ✅ Concurrent governance (`parallelMap`, order-preserving, bounded)
- ✅ Functional options (`WithReasoner`/`WithPolicyJudge`/`WithLM`/`WithMemoryCapacity`/…)
- ✅ Generics (`resolve[T]`)
- ✅ JSONL sink + range-over-func record iterator
- ✅ ScriptedLM deterministic test double (LLM path runs in CI, no network)
- ✅ **Budget alerts** (net-new, not in Python) — non-blocking progressive dollar-threshold alerts; **cap + thresholds authored in `memory.md` `

## 6. Category B — whole modules not started

**Accounting/reporting:** ✅ dollar costing (tokens × pricing) · ✅ usage report ledger

**Servers / UI / observability:** ⬜ `server` ⬜ `dashboard` ⬜ `monitor` ⬜ `web` ⬜ `mail`

**Distributed / infra / persistence:** ⬜ `kernel` ⬜ `k8s` ⬜ `sandbox` ⬜ `store` ✅ `session_store` ✅ `run` (as the `ear` CLI: run/repl/inspect/trail/usage/verify, governed exit codes) ⬜ `mcp_client` ⬜ `mcp_server` ⬜ `mcp_command_centre`

**Enterprise-AGI / governance / cognition planes:** ⬜ `enterprise` ⬜ `authority` ⬜ `compiler` ⬜ `journey` ⬜ `examiner` ✅ `knowledge` ⬜ `knowledge_governance` ⬜ `evolution` ⬜ `evolution_loop` ⬜ `optimizer` ⬜ `acquirer` ⬜ `coder` ⬜ `epistemic` ⬜ `adversary` ✅ `panel` ⬜ `goal` ✅ `spawner` ✅ `tool_binder` ⬜ `tools_cli` ⬜ `identity` ⬜ `task` ⬜ `exchange` ⬜ `thrift` ⬜ `carbon` ⬜ `energy` ⬜ `hardware` ⬜ `caveman` ⬜ `router`

**Reconceived / already covered:** 🔵 `parallel` → `parallelMap` · ✅ `approval` → `ApprovalVerdict`

---

## Recommended next order

1. ~~One `Stage` seam~~ ✅ **done** — the composable pipeline wires 9 of 13 signatures.
2. ~~Contract extraction~~ ✅ **done** — extract + `JudgeContractConformance` + hinted retry wired into the formalize stage; conformant data reaches the decision's evidence.
3. ~~LLM memory/adaptation~~ ✅ **done** — `SummarizeHistory` + `DistillInsight` wired with digest/most-common fallbacks.
4. ~~Dollar costing~~ ✅ **done** — `## Pricing` parsed, `Strategy.Dollars`, `~$X` on the usage record; plus a non-blocking `BudgetMonitor`.
5. ~~Tooling~~ ✅ **done** — governed tool binder + native reasoner tool-use loop (slices 1-2).
6. ~~Persistence~~ ✅ **done** — `SessionStore` (markdown + JSON codecs) with `## Cross-Session Data` authored path, restore-before-first-cycle + save-after-each-cycle wiring.
7. Then category-B planes as needed (MCP, server, store catalogue backends).

_Last reviewed: port through the composable `[]Stage` pipeline + typed generic signatures._
