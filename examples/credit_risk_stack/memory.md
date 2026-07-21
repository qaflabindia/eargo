# Memory & Strategy

The runtime's operating strategy, declared in plain English. Every setting
below is read out of this prose -- nothing is hardcoded in Python.

## Context History

Keep the 30 most recent cycles verbatim; compress anything older into
summaries so the reasoning context stays bounded as history grows.

## Cross-Session Data

Persist memory, experience and learned adaptations to `.ear/session.md`
so a new session picks up exactly where the last one left off.

## Subagent Spawning

Allow spawning up to 4 subagents, each scoped to a single persona. A
subagent shares the model and vocabulary but keeps its own memory.

## Model Selection

Reason with anthropic/claude-opus-4-8, reading the credential from
ANTHROPIC_API_KEY, at a temperature of 0.2. When the credential is absent
from the environment, the runtime stays on its deterministic fallback.

## MCP

- credit_bureau: pulls credit reports and score history, via `bureau-mcp-server`
- core_banking: reads account balances and repayment history, via `corebank-mcp-server`

## Tools

- amortization_calculator: computes the monthly payment for an amount, rate and term
- document_checker: verifies the application file carries every required document

## Reasoning Audit Trail

Log every reasoning step -- each policy judgment with its rationale,
process discovery, the deliberation with the full stacked prompt material,
and the explanation -- to `.ear/reasoning.md`, append-only across
sessions, so the trail can be reviewed and the stacked prompts optimised.

## Knowledge

The reference material the Librarian may consult and cite while
underwriting; sources resolve relative to this stack directory.

- underwriting manual: `knowledge/underwriting-manual.md`

## Skills Discovery

Rank processes by reading their descriptions against the intent, most
relevant first, and prefer a single best-fit process over a broad sweep.

## Ontological Settings

- risk grade: a letter from A to E, where A is the strongest credit and E the weakest
- debt-to-income: monthly debt obligations divided by gross monthly income
- decision: exactly one of approve or decline, never a hedge
