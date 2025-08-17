package config

import (
	"encoding/json"
	"strings"

	"github.com/charmbracelet/crush/internal/csync"
)

// EffectiveJSON returns a pretty-printed JSON representation of the current
// effective configuration. When redact is true, sensitive values are removed.
func (c *Config) EffectiveJSON(redactSensitive bool) ([]byte, error) {
	if !redactSensitive {
		// Include working dir wrapper for consistency with redacted output
		type export struct {
			*Config
			WorkingDir string `json:"working_dir"`
		}
		payload := export{Config: c, WorkingDir: c.WorkingDir()}
		return json.MarshalIndent(payload, "", "  ")
	}
	return c.redactedJSON()
}

// RedactedJSON returns a pretty-printed JSON representation of the current
// effective configuration with sensitive values removed.
func (c *Config) RedactedJSON() ([]byte, error) { return c.redactedJSON() }

func (c *Config) redactedJSON() ([]byte, error) {
	clone := &Config{
		Schema:              c.Schema,
		Models:              make(map[SelectedModelType]SelectedModel, len(c.Models)),
		Providers:           csync.NewMap[string, ProviderConfig](),
		MCP:                 make(MCPs, len(c.MCP)),
		LSP:                 make(LSPs, len(c.LSP)),
		Options:             nil,
		Permissions:         nil,
		LoadPathsConsidered: append([]string{}, c.LoadPathsConsidered...),
		LoadPathsLoaded:     append([]string{}, c.LoadPathsLoaded...),
	}

	for k, v := range c.Models {
		clone.Models[k] = v
	}

	// Providers (redact API keys and sensitive headers)
	for id, pc := range c.Providers.Seq2() {
		pc.APIKey = redact(pc.APIKey)
		if len(pc.ExtraHeaders) > 0 {
			pc.ExtraHeaders = sanitizeHeaders(pc.ExtraHeaders)
		}
		clone.Providers.Set(id, pc)
	}

	// MCP
	for name, m := range c.MCP {
		copy := m
		if len(copy.Env) > 0 {
			copy.Env = sanitizeEnv(copy.Env)
		}
		if len(copy.Headers) > 0 {
			copy.Headers = sanitizeHeaders(copy.Headers)
		}
		clone.MCP[name] = copy
	}

	// LSP (pass-through)
	for name, l := range c.LSP {
		clone.LSP[name] = l
	}

	// Options and Permissions are safe to copy as-is
	clone.Options = c.Options
	clone.Permissions = c.Permissions

	// Wrap with working dir for convenience in display
	type export struct {
		*Config
		WorkingDir string `json:"working_dir"`
	}
	payload := export{Config: clone, WorkingDir: c.WorkingDir()}
	return json.MarshalIndent(payload, "", "  ")
}

func redact(v string) string {
	if v == "" {
		return v
	}
	// Preserve placeholders like $ENV_VAR for readability; otherwise redact
	if strings.HasPrefix(v, "$") {
		return v
	}
	return "***REDACTED***"
}

func sanitizeHeaders(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		lk := strings.ToLower(k)
		if lk == "authorization" || strings.Contains(lk, "api-key") || strings.Contains(lk, "x-api-key") || strings.Contains(lk, "token") || strings.Contains(lk, "secret") {
			out[k] = redact(v)
			continue
		}
		out[k] = v
	}
	return out
}

func sanitizeEnv(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "key") || strings.Contains(lk, "token") || strings.Contains(lk, "secret") || strings.Contains(lk, "password") {
			out[k] = redact(v)
			continue
		}
		out[k] = v
	}
	return out
}
