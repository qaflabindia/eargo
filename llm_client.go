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
		text, retry, err := c.post(ctx, url, headers, payload, parse)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !retry {
			return "", err
		}
	}
	return "", fmt.Errorf("LM call failed after retries: %w", lastErr)
}

func (c *HTTPClient) post(ctx context.Context, url string, headers map[string]string, payload []byte, parse func(map[string]any) string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", false, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", true, err // transport failure: retryable
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return "", retry, fmt.Errorf("LM provider returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", false, fmt.Errorf("LM response was not JSON: %w", err)
	}
	return parse(decoded), false, nil
}

func (c *HTTPClient) request(prompt, system, cachePrefix string) (string, map[string]string, map[string]any, func(map[string]any) string) {
	if c.Provider == "anthropic" {
		return c.anthropic(prompt, system, cachePrefix)
	}
	return c.openai(prompt, system)
}

func (c *HTTPClient) anthropic(prompt, system, cachePrefix string) (string, map[string]string, map[string]any, func(map[string]any) string) {
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
	parse := func(data map[string]any) string {
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
		return sb.String()
	}
	return base + "/v1/messages", headers, body, parse
}

func (c *HTTPClient) openai(prompt, system string) (string, map[string]string, map[string]any, func(map[string]any) string) {
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
	parse := func(data map[string]any) string {
		choices, ok := data["choices"].([]any)
		if !ok || len(choices) == 0 {
			return ""
		}
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		text, _ := message["content"].(string)
		return text
	}
	return base + "/chat/completions", headers, body, parse
}
