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

// modelSpec is one parsed model selection: provider, "provider/model" id, the
// named credential env var, an optional local api_base, and the sampling
// params. Shared by the primary and auxiliary selections so both read prose by
// exactly the same rule.
type modelSpec struct {
	Provider        string
	Model           string
	APIKeyEnvVar    string
	APIBase         string
	Temperature     *float64
	MaxOutputTokens int
}

// parseModelProse reads a model selection from plain English. It never reads
// or stores a credential -- only the env var name that holds one.
func parseModelProse(prose string) modelSpec {
	var spec modelSpec
	if u := urlRe.FindString(prose); u != "" {
		spec.APIBase = strings.TrimRight(u, ".,;")
	}
	if m := envVarRe.FindStringSubmatch(prose); m != nil {
		spec.APIKeyEnvVar = m[1]
	}
	if m := temperatureRe.FindStringSubmatch(prose); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			spec.Temperature = &v
		}
	}
	if m := maxTokensRe.FindStringSubmatch(prose); m != nil {
		for _, g := range m[1:] {
			if g != "" {
				if v, err := strconv.Atoi(strings.ReplaceAll(g, ",", "")); err == nil {
					spec.MaxOutputTokens = v
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
			spec.Provider, spec.Model = left, left+"/"+right
			return spec
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
				spec.Provider, spec.Model = prov, prov+"/"+tok
				return spec
			}
		}
	}
	return spec
}

// clientFrom builds an HTTP LM client from a parsed spec, or (nil, false) when
// no model was named or -- the graceful-degradation case -- the named
// credential is absent and no local api_base is set. The key is read from the
// environment here; it is never logged.
func clientFrom(spec modelSpec) (*HTTPClient, bool) {
	if spec.Model == "" {
		return nil, false
	}
	client := NewHTTPClient(spec.Provider, spec.Model, spec.APIKeyEnvVar, spec.APIBase)
	if client.APIKey == "" && spec.APIBase == "" {
		return nil, false
	}
	client.Temperature = spec.Temperature
	if spec.MaxOutputTokens > 0 {
		client.MaxTokens = spec.MaxOutputTokens
	}
	return client, true
}

// readModel parses the primary model selection into the Strategy's fields.
func (s *Strategy) readModel(prose string) {
	s.ModelSelection = prose
	spec := parseModelProse(prose)
	s.Provider = spec.Provider
	s.Model = spec.Model
	s.APIKeyEnvVar = spec.APIKeyEnvVar
	s.APIBase = spec.APIBase
	s.Temperature = spec.Temperature
	s.MaxOutputTokens = spec.MaxOutputTokens
}

// readAuxiliaryModel parses the `## Auxiliary Model` selection -- a second,
// usually cheaper model for mechanical work (memory compression, adaptation
// distillation), not judgment -- into its own fields, by the same rule, so the
// two never collide.
func (s *Strategy) readAuxiliaryModel(prose string) {
	s.AuxModelSelection = prose
	spec := parseModelProse(prose)
	s.AuxProvider = spec.Provider
	s.AuxModel = spec.Model
	s.AuxAPIKeyEnvVar = spec.APIKeyEnvVar
	s.AuxAPIBase = spec.APIBase
	s.AuxTemperature = spec.Temperature
	s.AuxMaxOutputTokens = spec.MaxOutputTokens
}

// ModelClient builds the HTTP LM client this strategy declares, or (nil,
// false) when no model was named or -- the graceful-degradation case -- the
// named credential is absent from the environment and no local api_base is
// set, so the runtime stays on its deterministic fallback rather than
// crashing. The key is read from the environment here; it is never logged.
func (s *Strategy) ModelClient() (*HTTPClient, bool) {
	return clientFrom(modelSpec{
		Provider: s.Provider, Model: s.Model, APIKeyEnvVar: s.APIKeyEnvVar,
		APIBase: s.APIBase, Temperature: s.Temperature, MaxOutputTokens: s.MaxOutputTokens,
	})
}

// AuxModelClient builds the auxiliary (mechanical-work) LM client this
// strategy declares, by the same rule and with the same graceful degradation
// as ModelClient. Nil/false when no auxiliary model is authored or its
// credential is absent.
func (s *Strategy) AuxModelClient() (*HTTPClient, bool) {
	return clientFrom(modelSpec{
		Provider: s.AuxProvider, Model: s.AuxModel, APIKeyEnvVar: s.AuxAPIKeyEnvVar,
		APIBase: s.AuxAPIBase, Temperature: s.AuxTemperature, MaxOutputTokens: s.AuxMaxOutputTokens,
	})
}

func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
