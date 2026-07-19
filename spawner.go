package ear

import (
	"context"
	"fmt"
	"sync"
)

// Spawner spawns subagents: each a child Runtime scoped to one Persona,
// reasoning a single intent with the parent's model binding and strategy but
// its own memory, so a subagent's cycles never pollute the parent's history.
//
// Spawning is governed by memory.md's `## Subagent Spawning` section -- the
// strategy's prose decides whether spawning is allowed and how many subagents
// a runtime may spawn, and the Spawner enforces those limits the way the
// Governor enforces policies: by returning a SpawnDeniedError rather than
// silently proceeding. Nested spawns share the same Spawner, so a subagent
// that spawns its own subagents counts against the one budget too.
type Spawner struct {
	Enabled bool
	Limit   int // maximum subagents; 0 means unbounded

	mu      sync.Mutex
	spawned []*Runtime
}

// SpawnDeniedError is returned when the strategy forbids a spawn -- spawning
// disabled, or the subagent limit already reached. Like a policy block, it is
// surfaced, never swallowed.
type SpawnDeniedError struct{ Reason string }

func (e *SpawnDeniedError) Error() string { return e.Reason }

// Spawned returns the subagent runtimes spawned so far, in spawn order.
func (s *Spawner) Spawned() []*Runtime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Runtime{}, s.spawned...)
}

// Spawn builds a subagent scoped to persona, reasons intent through it, and
// returns the subagent's decision. The subagent shares the parent's LM, seams
// and Strategy -- reasoning with the same model and the same enterprise
// vocabulary -- but starts with fresh memory of its own. A forbidden spawn
// returns *SpawnDeniedError and reasons nothing.
func (s *Spawner) Spawn(ctx context.Context, parent *Runtime, persona *Persona, intent Intent) (any, error) {
	if s == nil || !s.Enabled {
		return nil, &SpawnDeniedError{Reason: "subagent spawning is disabled by the runtime's strategy"}
	}

	sub := s.build(parent, persona)

	// Reserve the slot atomically: the enabled/limit check and the append are
	// one critical section, so concurrent spawns can never overrun the limit.
	// Reasoning happens outside the lock -- it must not serialize subagents.
	s.mu.Lock()
	if s.Limit > 0 && len(s.spawned) >= s.Limit {
		s.mu.Unlock()
		return nil, &SpawnDeniedError{Reason: fmt.Sprintf("subagent limit of %d reached for runtime %q", s.Limit, parent.Name)}
	}
	s.spawned = append(s.spawned, sub)
	s.mu.Unlock()

	return sub.Reason(ctx, intent, nil)
}

// build assembles the child Runtime: a one-persona workflow wrapped in a
// process, sharing the parent's model and strategy but nothing of its memory.
func (s *Spawner) build(parent *Runtime, persona *Persona) *Runtime {
	workflow := (&Workflow{Name: persona.Name + " Subagent Workflow"}).AddPersona(persona)
	description := persona.Instructions
	if description == "" {
		description = "A subagent scoped to the persona " + persona.Name + "."
	}
	process := (&Process{Name: persona.Name + " Subagent", Description: description}).AddWorkflow(workflow)

	sub := NewRuntime(parent.Name + "::" + persona.Name)
	// Share the parent's model binding and reasoning seams, so the subagent
	// reasons with the same provider and the same deterministic fallbacks.
	sub.Reasoner = parent.Reasoner
	sub.PolicyJudge = parent.PolicyJudge
	sub.LM = parent.LM
	// Share the operating strategy (vocabulary, pricing, retention) but not
	// the memory layers -- those stay the subagent's own.
	sub.Strategy = parent.Strategy
	sub.Tools = parent.Tools
	// Nested spawns count against the same budget.
	sub.Spawner = s
	sub.AddProcess(process)
	return sub
}
