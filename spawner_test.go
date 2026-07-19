package ear

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSpawnReasonsThroughSubagent(t *testing.T) {
	parent := NewRuntime("Desk", WithSubagents(true, 0))
	persona := &Persona{Name: "Analyst", Instructions: "Assess the request."}

	decision, err := parent.Spawn(context.Background(), persona, NewIntent("assess a request", nil))
	if err != nil {
		t.Fatal(err)
	}
	if decision == nil {
		t.Error("expected a decision from the subagent")
	}
	spawned := parent.Spawner.Spawned()
	if len(spawned) != 1 {
		t.Fatalf("spawned = %d, want 1", len(spawned))
	}
	if spawned[0].Name != "Desk::Analyst" {
		t.Errorf("subagent name = %q", spawned[0].Name)
	}
}

func TestSubagentMemoryIsIsolated(t *testing.T) {
	parent := NewRuntime("Desk", WithSubagents(true, 0))
	persona := &Persona{Name: "Analyst", Instructions: "Assess."}

	if _, err := parent.Spawn(context.Background(), persona, NewIntent("first job", nil)); err != nil {
		t.Fatal(err)
	}
	// The subagent recorded its own cycle; the parent's memory stays untouched.
	if parent.Memory.Len() != 0 {
		t.Errorf("parent memory polluted by subagent: %d entries", parent.Memory.Len())
	}
	sub := parent.Spawner.Spawned()[0]
	if len(sub.Memory.Working) == 0 {
		t.Error("subagent should remember its own cycle")
	}
}

func TestSpawnDisabledIsDenied(t *testing.T) {
	parent := NewRuntime("Desk", WithSubagents(false, 0))
	_, err := parent.Spawn(context.Background(), &Persona{Name: "Analyst"}, NewIntent("go", nil))
	var denied *SpawnDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected SpawnDeniedError, got %v", err)
	}
	if len(parent.Spawner.Spawned()) != 0 {
		t.Error("a denied spawn must not record a subagent")
	}
}

func TestSpawnLimitEnforced(t *testing.T) {
	parent := NewRuntime("Desk", WithSubagents(true, 2))
	persona := &Persona{Name: "Analyst", Instructions: "Assess."}
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := parent.Spawn(ctx, persona, NewIntent("job", nil)); err != nil {
			t.Fatalf("spawn %d: %v", i, err)
		}
	}
	_, err := parent.Spawn(ctx, persona, NewIntent("one too many", nil))
	var denied *SpawnDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected the limit to deny the third spawn, got %v", err)
	}
	if len(parent.Spawner.Spawned()) != 2 {
		t.Errorf("spawned = %d, want the limit of 2", len(parent.Spawner.Spawned()))
	}
}

func TestNestedSpawnsShareTheLimit(t *testing.T) {
	parent := NewRuntime("Desk", WithSubagents(true, 1))
	persona := &Persona{Name: "Analyst", Instructions: "Assess."}
	ctx := context.Background()

	if _, err := parent.Spawn(ctx, persona, NewIntent("job", nil)); err != nil {
		t.Fatal(err)
	}
	// The subagent shares the parent's spawner, so its own spawn hits the
	// same, already-exhausted limit.
	sub := parent.Spawner.Spawned()[0]
	if sub.Spawner != parent.Spawner {
		t.Fatal("subagent should share the parent's spawner")
	}
	_, err := sub.Spawn(ctx, persona, NewIntent("nested", nil))
	var denied *SpawnDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("nested spawn should be denied by the shared limit, got %v", err)
	}
}

func TestSpawnLimitHoldsUnderConcurrency(t *testing.T) {
	parent := NewRuntime("Desk", WithSubagents(true, 3))
	persona := &Persona{Name: "Analyst", Instructions: "Assess."}
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = parent.Spawn(ctx, persona, NewIntent("job", nil))
		}()
	}
	wg.Wait()
	if got := len(parent.Spawner.Spawned()); got != 3 {
		t.Errorf("concurrent spawns overran the limit: %d, want 3", got)
	}
}

func TestLoaderWiresSpawnerFromMemoryMd(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("process.md", "# Desk\n\n## Handle\n\nHandle requests.\n\n- W\n\n## W\n\nDecide.\n")
	write("workflow.md", "## W\n\n1. Decide the request.\n")
	write("memory.md", "# Strategy\n\n## Subagent Spawning\n\nSpawn up to 2 subagents when a request needs a second opinion.\n")

	rt, err := LoadRuntime(dir, "Desk")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Spawner == nil {
		t.Fatal("memory.md declared subagent spawning but no Spawner was wired")
	}
	if !rt.Spawner.Enabled || rt.Spawner.Limit != 2 {
		t.Errorf("spawner = {Enabled:%v Limit:%d}, want {true 2}", rt.Spawner.Enabled, rt.Spawner.Limit)
	}
}

func TestLoaderDisablesSpawningWhenForbidden(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("process.md", "# Desk\n\n## Handle\n\nHandle requests.\n\n- W\n\n## W\n\nDecide.\n")
	write("workflow.md", "## W\n\n1. Decide.\n")
	write("memory.md", "# Strategy\n\n## Subagent Spawning\n\nNever spawn subagents; this runtime reasons alone.\n")

	rt, err := LoadRuntime(dir, "Desk")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Spawner == nil || rt.Spawner.Enabled {
		t.Fatalf("spawning should be disabled by the strategy, got %#v", rt.Spawner)
	}
}
