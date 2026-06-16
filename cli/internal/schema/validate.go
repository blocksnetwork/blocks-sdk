package schema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/deploy"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// contentTypeGuidance is appended to schema-validation errors that surface at
// /io/inputs/{n}/contentType, /io/outputs/{n}/contentType, or
// /io/inputs/{n}/accept/{m} so authors get the four-rule policy spelled out
// instead of the bare anyOf rejection text.
const contentTypeGuidance = " (contentType must be a value from the Blocks supported catalog, " +
	"a text/*, image/*, audio/*, or video/* subtype, " +
	"a */*+json, */*+xml, */*+zip, or */*+gzip suffix, " +
	"or application/octet-stream — lowercase, no parameters)"

var (
	contentTypeLocRe = regexp.MustCompile(`/contentType$`)
	acceptItemLocRe  = regexp.MustCompile(`/accept/\d+$`)
)

// The canonical schema lives at blocks-sdk/schemas/agent-card.schema.json.
// This local copy is kept in sync via go:generate before build.
//go:generate cp ../../../schemas/agent-card.schema.json schemas/agent-card.schema.json
//go:embed schemas/agent-card.schema.json
var agentCardSchemaJSON []byte

var agentNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// ValidationResult holds the outcome of a check run.
type ValidationResult struct {
	Errors    []string
	Successes []string
	Card      map[string]interface{}
}

// Validate performs all checks on the agent card at cardPath.
// It returns structured results so the caller can format output.
func Validate(cardPath string) ValidationResult {
	var res ValidationResult

	// 1. Check file exists
	if _, err := os.Stat(cardPath); os.IsNotExist(err) {
		res.Errors = append(res.Errors, fmt.Sprintf("agent-card.json not found at %s", cardPath))
		return res
	}
	res.Successes = append(res.Successes, "agent-card.json found")

	// 2. Parse JSON
	raw, err := os.ReadFile(cardPath)
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("Could not read file: %s", err))
		return res
	}

	return ValidateBytes(raw, cardPath)
}

// ValidateBytes performs all checks on the in-memory agent-card JSON `raw`,
// resolving relative paths (e.g. runtime.handler) against `cardPath`'s
// directory. Use this when the caller has already read the file and possibly
// mutated it (e.g. the publish-time `skills` → `tags` shim — see
// blocks-sdk/cli/cmd/publish_legacy_skills.go).
func ValidateBytes(raw []byte, cardPath string) ValidationResult {
	var res ValidationResult
	res.Successes = append(res.Successes, "agent-card.json found")

	var card map[string]interface{}
	if err := json.Unmarshal(raw, &card); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("Invalid JSON: %s", err))
		return res
	}
	res.Successes = append(res.Successes, "Valid JSON")
	res.Card = card

	// 3. JSON Schema validation
	schemaErrors := validateAgainstSchema(raw, card)
	if len(schemaErrors) > 0 {
		res.Errors = append(res.Errors, schemaErrors...)
	} else {
		res.Successes = append(res.Successes, "Passes JSON Schema validation")
	}

	// 4. Identity agentName check (Blocks-specific, beyond schema)
	identity, hasIdentity := card["identity"]
	if hasIdentity {
		if idMap, ok := identity.(map[string]interface{}); ok {
			if agentName, ok := idMap["agentName"].(string); ok && agentName != "" {
				if !agentNameRe.MatchString(agentName) {
					res.Errors = append(res.Errors, "identity.agentName must contain only alphanumeric characters and underscores")
				} else {
					res.Successes = append(res.Successes, fmt.Sprintf("identity.agentName: %s", agentName))
				}
			} else {
				res.Errors = append(res.Errors, "identity.agentName is required")
			}
		}
	}

	// 5. Runtime section checks (Blocks-specific, beyond schema)
	runtime, hasRuntime := card["runtime"]
	if !hasRuntime {
		res.Errors = append(res.Errors, "Missing runtime section (required for blocks run)")
	} else if rtMap, ok := runtime.(map[string]interface{}); ok {
		if handler, ok := rtMap["handler"].(string); ok && handler != "" {
			handlerPath := filepath.Join(filepath.Dir(cardPath), handler)
			if _, err := os.Stat(handlerPath); os.IsNotExist(err) {
				res.Errors = append(res.Errors, fmt.Sprintf("Handler not found: %s", handlerPath))
			} else {
				res.Successes = append(res.Successes, fmt.Sprintf("Handler found: %s", handler))
			}
		} else {
			res.Errors = append(res.Errors, "runtime.handler is required")
		}
	}

	// 6. ID uniqueness checks (cannot be enforced in JSON Schema)
	idErrors := checkIDUniqueness(card)
	if len(idErrors) > 0 {
		res.Errors = append(res.Errors, idErrors...)
	} else {
		res.Successes = append(res.Successes, "All IDs are unique")
	}

	// 7. identity.webApps[].url semantic validation. The JSON Schema regex
	// is a coarse-grained shape gate; it can't enforce port-range bounds,
	// percent-encoding validity, or IPv6 literal correctness. Parse each
	// URL through the canonical helper so `blocks check` rejects anything
	// the backend Zod validator would reject at registration time.
	webAppErrors := checkWebAppURLs(card)
	if len(webAppErrors) > 0 {
		res.Errors = append(res.Errors, webAppErrors...)
	}

	return res
}

// checkWebAppURLs runs `deploy.ValidateWebAppURL` against each
// identity.webApps[].url so the CLI surfaces parser-level errors
// (invalid percent-encoding, out-of-range port, malformed IPv6) before
// the user runs `blocks publish` and discovers the backend reject.
func checkWebAppURLs(card map[string]interface{}) []string {
	identity, ok := card["identity"].(map[string]interface{})
	if !ok {
		return nil
	}
	rawList, ok := identity["webApps"].([]interface{})
	if !ok {
		return nil
	}
	var errs []string
	for i, entry := range rawList {
		obj, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		urlStr, ok := obj["url"].(string)
		if !ok {
			continue
		}
		if err := deploy.ValidateWebAppURL(urlStr); err != nil {
			errs = append(errs, fmt.Sprintf("identity.webApps[%d].%s", i, err.Error()))
		}
	}
	return errs
}

// checkIDUniqueness validates that io.inputs[].id, io.outputs[].id, and tags[].id
// contain no duplicates within their respective arrays.
func checkIDUniqueness(card map[string]interface{}) []string {
	var errs []string

	if ioSection, ok := card["io"].(map[string]interface{}); ok {
		if inputs, ok := ioSection["inputs"].([]interface{}); ok {
			errs = append(errs, findDuplicateIDs(inputs, "io.inputs")...)
		}
		if outputs, ok := ioSection["outputs"].([]interface{}); ok {
			errs = append(errs, findDuplicateIDs(outputs, "io.outputs")...)
		}
	}

	if tags, ok := card["tags"].([]interface{}); ok {
		errs = append(errs, findDuplicateIDs(tags, "tags")...)
	}

	return errs
}

// findDuplicateIDs checks a slice of objects for duplicate "id" fields.
func findDuplicateIDs(items []interface{}, path string) []string {
	var errs []string
	seen := make(map[string]bool)
	for _, item := range items {
		obj, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		id, ok := obj["id"].(string)
		if !ok || id == "" {
			continue
		}
		if seen[id] {
			errs = append(errs, fmt.Sprintf("Duplicate id %q in %s", id, path))
		}
		seen[id] = true
	}
	return errs
}

func validateAgainstSchema(cardJSON []byte, card map[string]interface{}) []string {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("agent-card.schema.json", strings.NewReader(string(agentCardSchemaJSON))); err != nil {
		return []string{fmt.Sprintf("Internal error loading schema: %s", err)}
	}

	sch, err := compiler.Compile("agent-card.schema.json")
	if err != nil {
		return []string{fmt.Sprintf("Internal error compiling schema: %s", err)}
	}

	var v interface{}
	if err := json.Unmarshal(cardJSON, &v); err != nil {
		return []string{fmt.Sprintf("JSON parse error: %s", err)}
	}

	if err := sch.Validate(v); err != nil {
		ve, ok := err.(*jsonschema.ValidationError)
		if !ok {
			return []string{err.Error()}
		}
		// Translate known transport-class invariant failures into the same
		// human-readable text the backend Zod validator emits, then drop
		// the raw class-machinery noise for the inputs we covered.
		friendly, handled := auditInputInvariants(card)
		raw := flattenValidationErrors(ve, handled)
		return dedupeStrings(append(friendly, raw...))
	}

	return nil
}

func flattenValidationErrors(ve *jsonschema.ValidationError, handled map[int]map[string]struct{}) []string {
	var errs []string
	if ve.Message != "" && len(ve.Causes) == 0 {
		loc := ve.InstanceLocation
		if loc == "" {
			loc = "(root)"
		}
		if shouldSuppressRaw(loc, ve.Message, handled) {
			return errs
		}
		msg := ve.Message
		if isContentTypeLocation(loc) {
			msg += contentTypeGuidance
		}
		errs = append(errs, fmt.Sprintf("Schema: %s — %s", loc, msg))
	}
	for _, cause := range ve.Causes {
		errs = append(errs, flattenValidationErrors(cause, handled)...)
	}
	return errs
}

func dedupeStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// isContentTypeLocation matches instance locations whose final segment is the
// contentType field or an accept[] index, regardless of inputs vs outputs.
func isContentTypeLocation(loc string) bool {
	return contentTypeLocRe.MatchString(loc) || acceptItemLocRe.MatchString(loc)
}
