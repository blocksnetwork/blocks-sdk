package auth

import "strings"

const (
	apiKeyNamePrefix = "cli-"
	// apiKeyNameMaxLen mirrors the backend's maximumNameLength in
	// afui_mvp_backend/src/lib/auth.ts so two machines with long shared
	// hostname prefixes don't silently collapse to the same key name.
	apiKeyNameMaxLen   = 255
	apiKeyNameFallback = "cli-default"
)

// BuildApiKeyName derives a Better-Auth-compatible API key name from an OS
// hostname. It strips the macOS ".local" Bonjour suffix, replaces disallowed
// characters with '-', collapses dash runs, trims leading/trailing dashes,
// caps total length at the server's limit, and falls back to "cli-default"
// when the sanitized hostname is empty.
func BuildApiKeyName(hostname string) string {
	h := strings.TrimSpace(hostname)
	h = strings.TrimSuffix(h, ".local")

	var b strings.Builder
	b.Grow(len(h))
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	sanitized := b.String()
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}
	sanitized = strings.Trim(sanitized, "-.")
	if sanitized == "" {
		return apiKeyNameFallback
	}

	name := apiKeyNamePrefix + sanitized
	if len(name) > apiKeyNameMaxLen {
		name = strings.TrimRight(name[:apiKeyNameMaxLen], "-.")
	}
	return name
}
