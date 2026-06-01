package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/pubnub/blocks-sdk/cli/internal/schema"
)

// validateCardWithLegacyShim is the shared entry point used by `blocks run`,
// `blocks check`, and `blocks publish`. It reads the card off disk, runs the
// legacy `skills` → `tags` shim (warning to stderr if a legacy field is
// present), and validates the resulting bytes against the agent-card schema.
//
// Returning ValidationResult lets each caller render successes/errors in
// whatever format it currently uses, instead of duplicating the read +
// preprocess + validate dance.
func validateCardWithLegacyShim(cardPath string) schema.ValidationResult {
	raw, err := os.ReadFile(cardPath)
	if err != nil {
		// Not-found / permission errors flow through schema.Validate which
		// owns the canonical "agent-card.json not found" message.
		return schema.Validate(cardPath)
	}
	preprocessed, preErr := preprocessAgentCard(raw, cardPath, os.Stderr)
	if preErr != nil {
		// Invalid JSON: fall back so the canonical "Invalid JSON" error has
		// a single source of truth (schema.ValidateBytes).
		preprocessed = raw
	}
	return schema.ValidateBytes(preprocessed, cardPath)
}

// preprocessAgentCard is the CLI-side compatibility shim for the legacy
// `skills` → `tags` field rename. The wire / SDK / schema layers are a clean
// break — no `skills` alias anywhere — but a developer using `blocks publish`
// against a stale `agent-card.json` still on disk would otherwise see a
// confusing schema rejection. This function rewrites the deprecated field
// in-memory (the source file is NOT mutated) and prints a one-shot
// deprecation warning to stderr so they know to update the file.
//
// Return semantics:
//   - skills-only card → rename to tags, warn, return modified bytes.
//   - tags-only card   → no warning, return raw bytes unchanged (byte-equal).
//   - both present     → keep tags, drop skills, warn about the conflict.
//   - neither present  → return raw bytes unchanged (schema validation will
//     surface the missing-required-field error downstream).
//   - invalid JSON     → return raw bytes + an error; the caller's downstream
//     validation produces the canonical "Invalid JSON" failure.
//
// Stability: when the skills-present path rewrites the card, output bytes
// are produced via json.Marshal of a map and are NOT a stable reformat —
// key order, whitespace, duplicate keys, and numeric precision may change.
// Callers must not write these bytes back to disk; they are intended to
// flow into downstream validation only.
func preprocessAgentCard(raw []byte, sourcePath string, warnTo io.Writer) ([]byte, error) {
	var card map[string]interface{}
	if err := json.Unmarshal(raw, &card); err != nil {
		return raw, fmt.Errorf("parse agent-card: %w", err)
	}
	_, hasSkills := card["skills"]
	_, hasTags := card["tags"]
	if !hasSkills {
		return raw, nil
	}
	if hasTags {
		fmt.Fprintf(warnTo,
			"[blocks-cli] agent-card has both `tags` and `skills`; using `tags` and ignoring `skills`. File: %s\n"+
				"  This shim will be removed in a future CLI release — drop the `skills` key from your agent-card.json.\n",
			sourcePath)
		delete(card, "skills")
	} else {
		fmt.Fprintf(warnTo,
			"[blocks-cli] DEPRECATED: agent-card field `skills` was renamed to `tags`.\n"+
				"  File: %s\n"+
				"  Action: rename `skills` → `tags` in your agent-card.json. Same shape, just the key name changes.\n"+
				"  This shim will be removed in a future CLI release; programmatic SDK callers (`@blocks-network/sdk`, `blocks-network` on PyPI) get no shim and must rename now.\n",
			sourcePath)
		card["tags"] = card["skills"]
		delete(card, "skills")
	}
	return json.Marshal(card)
}
