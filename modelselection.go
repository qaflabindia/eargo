package ear

import (
	"regexp"
	"strconv"
	"strings"
)

// Model selection is authored in memory.md, not coded: the provider, model
// id, temperature and max-output-tokens are read from plain English, and the
// API key is read from the *named* environment variable at load time -- the
// key value never appears in markdown or in code. This mirrors the Python
// package's Strategy._parse_model_prose + model_binding().

var (
	envVarRe      = regexp.MustCompile(`\b([A-Z][A-Z0-9_]*(?:KEY|TOKEN)[A-Z0-9_]*)\b`)
	temperatureRe = regexp.MustCompile(`(?i)temperature\s*(?:of|=|:|at)?\s*([0-9]*\.?[0-9]+)`)
	maxTokensRe   = regexp.MustCompile(`(?i)max[_ ]?tokens\s*(?:of|=|:)?\s*([\d,]+)|max(?:imum)?\s+(?:output|response|reply)(?:\s+length)?\s*(?:of|=|:)?\s*([\d,]+)\s*tokens?|up\s+to\s*(?:of|=|:)?\s*([\d,]+)\s*tokens?`)
	modelIDRe     = regexp.MustCompile(`([A-Za-z][\w-]*)/([A-Za-z][\w.:-]*)`)
	modelTokenRe  = regexp.MustCompile(`\b([a-z][a-z0-9]*(?:[-.:][a-z0-9]+)+)\b`)
)

// providers is the recognized set, in a fixed order so the fallback
// provider-word scan is deterministic.
var providers = []string{
	"anthropic", "openai", "azure", "gemini", "vertex", "google", "bedrock",
	"mistral", "cohere", "groq", "together", "ollama", "deepseek", "vllm",
}

func isProvider(name string) bool {
	for _, p := range providers {
		if p == name {
			return true
		}
	}
	return false
}

// readModel parses the model-selection prose into the Strategy's fields. It
// never reads or stores a credential -- only the env var name that holds one.
func (s *Strategy) readModel(prose string) {
	s.ModelSelection = prose

	if u := urlRe.FindString(prose); u != "" {
		s.APIBase = strings.TrimRight(u, ".,;")
	}
	if m := envVarRe.FindStringSubmatch(prose); m != nil {
		s.APIKeyEnvVar = m[1]
	}
	if m := temperatureRe.FindStringSubmatch(prose); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			s.Temperature = &v
		}
	}
	if m := maxTokensRe.FindStringSubmatch(prose); m != nil {
		for _, g := range m[1:] {
			if g != "" {
				if v, err := strconv.Atoi(strings.ReplaceAll(g, ",", "")); err == nil {
					s.MaxOutputTokens = v
				}
				break
			}
		}
	}

	// A model id written "provider/model" -- accepted when the left side is a
	// known provider or the right side carries a digit (so "approve/decline"
	// prose is never mistaken for one). URLs are removed first so an api_base
	// can't be read as a model id.
	cleaned := urlRe.ReplaceAllString(prose, " ")
	for _, m := range modelIDRe.FindAllStringSubmatch(cleaned, -1) {
		left, right := strings.ToLower(m[1]), strings.TrimRight(m[2], ".")
		if isProvider(left) || hasDigit(right) {
			s.Provider, s.Model = left, left+"/"+right
			return
		}
	}
	// Fallback: a named provider plus a model-like token carrying a digit.
	lowered := strings.ToLower(prose)
	for _, prov := range providers {
		if !regexp.MustCompile(`\b` + prov + `\b`).MatchString(lowered) {
			continue
		}
		for _, tok := range modelTokenRe.FindAllString(lowered, -1) {
			if tok != prov && hasDigit(tok) {
				s.Provider, s.Model = prov, prov+"/"+tok
				return
			}
		}
	}
}

// ModelClient builds the HTTP LM client this strategy declares, or (nil,
// false) when no model was named or -- the graceful-degradation case -- the
// named credential is absent from the environment and no local api_base is
// set, so the runtime stays on its deterministic fallback rather than
// crashing. The key is read from the environment here; it is never logged.
func (s *Strategy) ModelClient() (*HTTPClient, bool) {
	if s.Model == "" {
		return nil, false
	}
	client := NewHTTPClient(s.Provider, s.Model, s.APIKeyEnvVar, s.APIBase)
	if client.APIKey == "" && s.APIBase == "" {
		return nil, false
	}
	client.Temperature = s.Temperature
	if s.MaxOutputTokens > 0 {
		client.MaxTokens = s.MaxOutputTokens
	}
	return client, true
}

func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
