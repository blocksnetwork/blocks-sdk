package deploy

import (
	"fmt"
	"net/url"
	"strconv"
)

// ValidateWebAppURL enforces the same shape rule as
// `identity.webApps[].url` in `schemas/agent-card.schema.json` (v4.1.3),
// plus the semantic checks the JSON Schema regex cannot express. Used by
// the post-deploy plugin stdout validator and the `blocks check` CLI step
// so a card that fails registration on the backend never gets that far.
//
// Rules:
//   - Must parse as an absolute URL with non-empty hostname.
//   - Scheme is "https", OR "http" with hostname in {localhost, 127.0.0.1, ::1}.
//   - Port (when present) must be 1-65535. Go's url.Parse accepts 99999
//     and beyond, so the range is enforced here.
//   - Reject obvious percent-encoding corruption (`%` not followed by two
//     hex digits). url.Parse is permissive about this.
func ValidateWebAppURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("url is empty")
	}
	if err := checkPercentEncoding(raw); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url %q is not a valid URL: %w", raw, err)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("url %q has no host", raw)
	}
	// Go's url.Parse already rejects malformed IPv6 literals
	// (`https://[x]`, `https://[gggg::1]`) with "invalid host" via the
	// netip package — no extra check is needed at this layer for IPv6.
	// The fall-through scheme check below restricts `[::1]` to the http
	// loopback arm.
	if portStr := parsed.Port(); portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("url %q has invalid port %q (must be 1-65535)", raw, portStr)
		}
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return nil
		}
		return fmt.Errorf("url %q: http:// is only allowed for loopback hosts (localhost, 127.0.0.1, [::1])", raw)
	default:
		return fmt.Errorf("url %q: scheme %q is not allowed; must be https or http (loopback only)", raw, parsed.Scheme)
	}
}

// checkPercentEncoding rejects URLs containing `%` that is not followed by
// two hex digits. url.Parse leaves these intact rather than erroring.
func checkPercentEncoding(raw string) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '%' {
			continue
		}
		if i+2 >= len(raw) || !isHex(raw[i+1]) || !isHex(raw[i+2]) {
			return fmt.Errorf("url %q contains invalid percent-encoding at offset %d", raw, i)
		}
	}
	return nil
}

func isHex(b byte) bool {
	switch {
	case b >= '0' && b <= '9':
		return true
	case b >= 'a' && b <= 'f':
		return true
	case b >= 'A' && b <= 'F':
		return true
	}
	return false
}

