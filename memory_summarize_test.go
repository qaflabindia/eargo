package ear

import (
	"errors"
	"strings"
	"testing"
)

func TestMemoryLLMSummarizer(t *testing.T) {
	m := &Memory{Capacity: 2}
	m.Summarizer = func(history string) (string, error) {
		return "MODEL SUMMARY", nil
	}
	for i := 0; i < 4; i++ {
		m.Record("intent", "decision", nil, nil)
	}
	if len(m.Compressed) == 0 || !strings.HasPrefix(m.Compressed[0], "MODEL SUMMARY") {
		t.Errorf("expected model-written summary, got %v", m.Compressed)
	}
}

func TestMemorySummarizerFallsBackOnError(t *testing.T) {
	m := &Memory{Capacity: 2}
	m.Summarizer = func(string) (string, error) { return "", errors.New("model down") }
	for i := 0; i < 4; i++ {
		m.Record("intent", "decline", nil, nil)
	}
	if len(m.Compressed) == 0 || !strings.Contains(m.Compressed[0], "earlier cycles") {
		t.Errorf("expected deterministic-digest fallback, got %v", m.Compressed)
	}
}

func TestAdaptationLLMDistiller(t *testing.T) {
	x := NewExperience()
	for i := 0; i < 3; i++ {
		x.ObserveEntry(MemoryEntry{Decision: "approve"})
	}
	bank := NewAdaptationBank()
	bank.Distiller = func(summary string) (string, error) { return "DISTILLED INSIGHT", nil }
	a := bank.LearnFrom(x)
	if a == nil || a.Insight != "DISTILLED INSIGHT" {
		t.Errorf("expected model-distilled insight, got %+v", a)
	}
}

func TestAdaptationDistillerFallsBackOnError(t *testing.T) {
	x := NewExperience()
	x.ObserveEntry(MemoryEntry{Decision: "decline"})
	bank := NewAdaptationBank()
	bank.Distiller = func(string) (string, error) { return "", errors.New("model down") }
	a := bank.LearnFrom(x)
	if a == nil || !strings.Contains(a.Insight, "Most frequent outcome") {
		t.Errorf("expected deterministic fallback, got %+v", a)
	}
}
