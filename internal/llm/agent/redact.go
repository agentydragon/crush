package agent

import (
	"strings"

	"github.com/charmbracelet/crush/internal/config"
)

func buildRedactions() []string {
	var out []string
	cfg := config.Get()
	for p := range cfg.Providers.Seq() {
		if p.APIKey != "" {
			if v, err := cfg.Resolve(p.APIKey); err == nil && v != "" {
				out = append(out, v)
				out = append(out, "Bearer "+v)
			}
		}
		for k, v := range p.ExtraHeaders {
			keyLower := strings.ToLower(k)
			if strings.Contains(keyLower, "authorization") || strings.Contains(keyLower, "api") || strings.Contains(keyLower, "token") || strings.Contains(keyLower, "key") || strings.Contains(keyLower, "secret") {
				if v != "" {
					if resolved, err := cfg.Resolve(v); err == nil && resolved != "" {
						out = append(out, resolved)
					}
				}
			}
		}
	}
	return out
}

func redactText(s string, secrets []string) string {
	if s == "" || len(secrets) == 0 {
		return s
	}
	redacted := s
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, secret, "••••")
	}
	return redacted
}
