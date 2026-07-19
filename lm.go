package ear

import (
	"context"
	"strings"
	"sync"
)

// LM is the language-model seam the judgment engine calls. Complete renders a
// completion for a prompt with an optional system instruction and a
// provider-neutral cache hint (the stable leading span of prompt that repeats
// across calls; empty means no hint). It takes a context.Context so a call
// can be cancelled or deadline-bound. The real client is HTTPClient
// (llm_client.go); ScriptedLM is the deterministic test double.
type LM interface {
	Complete(ctx context.Context, prompt, system, cachePrefix string) (string, error)
}

// Call is one recorded LM interaction, mirroring the Python package's history
// entries so per-cycle usage accounting can be layered on later.
type Call struct {
	Prompt      string
	System      string
	CachePrefix string
	Reply       string
}

// ScriptedLM is a deterministic LM for tests and offline demos. It answers
// from Replies in order; once exhausted it returns Default. Every call is
// recorded in History. It performs no I/O, so it is safe and instant in
// tests -- the whole point of the seam being an interface.
type ScriptedLM struct {
	mu      sync.Mutex
	Replies []string
	Default string
	History []Call
	next    int
}

// Complete returns the next scripted reply (or Default), recording the call.
func (s *ScriptedLM) Complete(_ context.Context, prompt, system, cachePrefix string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := s.Default
	if s.next < len(s.Replies) {
		reply = s.Replies[s.next]
		s.next++
	}
	s.History = append(s.History, Call{Prompt: prompt, System: system, CachePrefix: cachePrefix, Reply: reply})
	return reply, nil
}

// section renders a single "## Name\nvalue" block -- a convenience for tests
// building a scripted markdown reply the judgment parser will read back.
func section(name, value string) string {
	return "## " + name + "\n\n" + strings.TrimSpace(value) + "\n"
}

// Reply assembles scripted output sections into one markdown reply, in the
// order given as name,value,name,value,... A convenience for tests.
func Reply(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		b.WriteString(section(pairs[i], pairs[i+1]))
		b.WriteString("\n")
	}
	return b.String()
}
