// Package branding holds the CLI's set-once product name. It is configured at
// command startup from the active profile's product name (see cmd/root.go
// PersistentPreRun), and read by interactive prompts and success output. Read
// without Set() it yields the stock default, so behavior is unchanged
// off-enterprise.
package branding

const defaultProductName = "Blocks Network"

// productName is set once at command startup from the active profile.
var productName = defaultProductName

// Set overrides the product name (call once at startup with the active
// profile's name). An empty name is ignored so callers can pass an unset value
// without clobbering the default.
func Set(name string) {
	if name != "" {
		productName = name
	}
}

// Reset restores the default (tests).
func Reset() { productName = defaultProductName }

// ProductName is the brand name to show in help, prompts, and success output.
func ProductName() string { return productName }
