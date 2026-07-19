package ear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// HTTPClient is EAR's own dependency-free LM client, built on net/http and
// encoding/json alone -- no provider SDK, no LiteLLM, no DSPy, matching the
// Python package's llm.py. It speaks Anthropic's Messages API natively and
// any OpenAI-compatible endpoint (Azure, Ollama, vLLM, ...), chosen by
// Provider. Credentials are read from the environment, never hardcoded.
type HTTPClient struct {
	Provider    string // "anthropic" or anything else (OpenAI-compatible)
	Model       string // "claude-opus-4-8" or "provider/model"; the prefix is stripped on the wire
	APIKey      string
	APIBase     string
	Temperature *float64
	MaxTokens   int
	HTTP        *http.Client

	mu      sync.Mutex
	history []Call
}

// Calls returns a snapshot of this client's call history, so the Runtime can
// account a cycle's model calls, tokens and latency from the delta.
func (c *HTTPClient) Calls() []Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Call{}, c.history...)
}

func (c *HTTPClient) record(call Call) {
	c.mu.Lock()
	c.history = append(c.history, call)
	c.mu.Unlock()
}

const (
	defaultMaxTokens = 2048
	anthropicVersion = "2023-06-01"
)

var retryBackoff = []time.Duration{time.Second, 2 * time.Second}

// NewHTTPClient builds a client for a provider/model, reading the API key from
// keyEnvVar (or, if empty, "<PROVIDER>_API_KEY"). apiBase may be "" for the
// provider default.
func NewHTTPClient(provider, model, keyEnvVar, apiBase string) *HTTPClient {
	if keyEnvVar == "" {
		keyEnvVar = strings.ToUpper(provider) + "_API_KEY"
	}
	return &HTTPClient{
		Provider: provider,
		Model:    model,
		APIKey:   os.Getenv(keyEnvVar),
		APIBase:  apiBase,
		HTTP:     &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *HTTPClient) bareModel() string {
	if _, after, ok := strings.Cut(c.Model, "/"); ok {
		return after
	}
	return c.Model
}

func (c *HTTPClient) maxTokens() int {
	if c.MaxTokens > 0 {
		return c.MaxTokens
	}
	return defaultMaxTokens
}

// Complete implements LM against the configured provider, with bounded
// retries on transport failure and 5xx/429 responses.
func (c *HTTPClient) Complete(ctx context.Context, prompt, system, cachePrefix string) (string, error) {
	url, headers, body, parse := c.request(prompt, system, cachePrefix)
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	started := time.Now()
	var lastErr error
	for attempt := 0; attempt <= len(retryBackoff); attempt++ {
		if attempt > 0 {
			wait := retryBackoff[min(attempt-1, len(retryBackoff)-1)]
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
		}
		text, usage, retry, err := c.post(ctx, url, headers, payload, parse)
		if err == nil {
			c.record(Call{
				Prompt: prompt, System: system, CachePrefix: cachePrefix, Reply: text,
				Usage: usage, LatencyMs: time.Since(started).Milliseconds(), Retries: attempt,
			})
			return text, nil
		}
		lastErr = err
		if !retry {
			return "", err
		}
	}
	return "", fmt.Errorf("LM call failed after retries: %w", lastErr)
}

func (c *HTTPClient) post(ctx context.Context, url string, headers map[string]string, payload []byte, parse func(map[string]any) (string, Usage)) (string, Usage, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", Usage{}, false, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", Usage{}, true, err // transport failure: retryable
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", Usage{}, retry, fmt.Errorf("LM provider returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", Usage{}, false, fmt.Errorf("LM response was not JSON: %w", err)
	}
	text, usage := parse(decoded)
	return text, usage, false, nil
}

func (c *HTTPClient) request(prompt, system, cachePrefix string) (string, map[string]string, map[string]any, func(map[string]any) (string, Usage)) {
	if c.Provider == "anthropic" {
		return c.anthropic(prompt, system, cachePrefix)
	}
	return c.openai(prompt, system)
}

func (c *HTTPClient) anthropic(prompt, system, cachePrefix string) (string, map[string]string, map[string]any, func(map[string]any) (string, Usage)) {
	base := c.APIBase
	if base == "" {
		base = "https://api.anthropic.com"
	}
	headers := map[string]string{
		"content-type":      "application/json",
		"anthropic-version": anthropicVersion,
		"x-api-key":         c.APIKey,
	}
	var content any = prompt
	if cachePrefix != "" && strings.HasPrefix(prompt, cachePrefix) && len(cachePrefix) < len(prompt) {
		content = []any{
			map[string]any{"type": "text", "text": cachePrefix, "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": prompt[len(cachePrefix):]},
		}
	}
	body := map[string]any{
		"model":      c.bareModel(),
		"max_tokens": c.maxTokens(),
		"messages":   []any{map[string]any{"role": "user", "content": content}},
	}
	if system != "" {
		body["system"] = system
	}
	if c.Temperature != nil {
		body["temperature"] = *c.Temperature
	}
	parse := func(data map[string]any) (string, Usage) {
		var sb strings.Builder
		if blocks, ok := data["content"].([]any); ok {
			for _, b := range blocks {
				if m, ok := b.(map[string]any); ok && m["type"] == "text" {
					if t, ok := m["text"].(string); ok {
						sb.WriteString(t)
					}
				}
			}
		}
		u, _ := data["usage"].(map[string]any)
		return sb.String(), Usage{
			PromptTokens:     jsonInt(u["input_tokens"]),
			CompletionTokens: jsonInt(u["output_tokens"]),
			CacheReadTokens:  jsonInt(u["cache_read_input_tokens"]),
			CacheWriteTokens: jsonInt(u["cache_creation_input_tokens"]),
		}
	}
	return base + "/v1/messages", headers, body, parse
}

func (c *HTTPClient) openai(prompt, system string) (string, map[string]string, map[string]any, func(map[string]any) (string, Usage)) {
	base := c.APIBase
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	headers := map[string]string{
		"content-type":  "application/json",
		"authorization": "Bearer " + c.APIKey,
	}
	var messages []any
	if system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}
	messages = append(messages, map[string]any{"role": "user", "content": prompt})
	body := map[string]any{"model": c.bareModel(), "messages": messages}
	if c.Temperature != nil {
		body["temperature"] = *c.Temperature
	}
	if c.MaxTokens > 0 {
		body["max_tokens"] = c.MaxTokens
	}
	parse := func(data map[string]any) (string, Usage) {
		choices, ok := data["choices"].([]any)
		if !ok || len(choices) == 0 {
			return "", Usage{}
		}
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		text, _ := message["content"].(string)
		u, _ := data["usage"].(map[string]any)
		details, _ := u["prompt_tokens_details"].(map[string]any)
		return text, Usage{
			PromptTokens:     jsonInt(u["prompt_tokens"]),
			CompletionTokens: jsonInt(u["completion_tokens"]),
			CacheReadTokens:  jsonInt(details["cached_tokens"]),
		}
	}
	return base + "/chat/completions", headers, body, parse
}

// jsonInt reads an integer from a decoded-JSON value (numbers arrive as
// float64), returning 0 for a missing or non-numeric value.
func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
