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
