// Package branding holds the CLI's set-once product name. It is configured at
// command startup from the active profile's product name (see cmd/root.go
// PersistentPreRun), and read by interactive prompts and success output. Read
// without Set() it yields the stock default, so behavior is unchanged
// off-enterprise.
package branding

import "github.com/pubnub/blocks-sdk/cli/internal/termsafe"

const defaultProductName = "Blocks Network"

// productName is set once at command startup from the active profile.
var productName = defaultProductName

// Set overrides the product name (call once at startup with the active
// profile's name). An empty name is ignored so callers can pass an unset value
// without clobbering the default.
//
// The name is escaped here rather than at each print site. It arrives from the
// deployment — the profile's cached copy, or the cli-config discovery response —
// so it is remote input that reaches a terminal, and it is interpolated into
// prompts, help text, banners and success lines across the CLI. Escaping at the
// print sites made safety opt-in: every new interpolation had to remember, and the
// ones that forgot could erase or forge the very confirmation the user was reading.
// One escape at the single point of entry makes ProductName() safe by construction,
// so a caller cannot reintroduce the hole by printing it plainly.
func Set(name string) {
	if name != "" {
		productName = termsafe.Text(name)
	}
}

// Reset restores the default (tests).
func Reset() { productName = defaultProductName }

// Default is the stock product name, independent of whatever Set() recorded. A
// command that has established it is acting on the public deployment needs this
// rather than ProductName(), which may carry a brand configured for a different
// deployment entirely.
func Default() string { return defaultProductName }

// ProductName is the brand name to show in help, prompts, and success output.
func ProductName() string { return productName }
