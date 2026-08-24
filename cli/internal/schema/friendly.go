package schema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"sync"
)

// transportClass mirrors the backend Zod TransportClass and the canonical
// schema's three-way classification of input contentTypes.
type transportClass string

const (
	transportForm    transportClass = "form"
	transportText    transportClass = "text"
	transportFile    transportClass = "file"
	transportUnknown transportClass = ""
)

// schemaCatalogs holds the three transport-class enums extracted from the
// embedded canonical agent-card schema. Loaded once and cached.
type schemaCatalogs struct {
	form map[string]struct{}
	text map[string]struct{}
	file map[string]struct{}
}

var (
	catalogsOnce sync.Once
	catalogsVal  schemaCatalogs
	catalogsErr  error
)

// catalogs returns the three transport-class catalogs. The result is parsed
// from the embedded canonical schema on first call so the CLI cannot drift
// from the source of truth.
func catalogs() (schemaCatalogs, error) {
	catalogsOnce.Do(func() {
		catalogsVal, catalogsErr = loadSchemaCatalogs(agentCardSchemaJSON)
	})
	return catalogsVal, catalogsErr
}

func loadSchemaCatalogs(raw []byte) (schemaCatalogs, error) {
	var doc struct {
		Defs map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return schemaCatalogs{}, fmt.Errorf("parse embedded schema: %w", err)
	}
	pick := func(name string) (map[string]struct{}, error) {
		def, ok := doc.Defs[name]
		if !ok || len(def.Enum) == 0 {
			return nil, fmt.Errorf("missing $defs.%s.enum in embedded schema", name)
		}
		out := make(map[string]struct{}, len(def.Enum))
		for _, v := range def.Enum {
			out[v] = struct{}{}
		}
		return out, nil
	}
	form, err := pick("catalogFormTypes")
	if err != nil {
		return schemaCatalogs{}, err
	}
	text, err := pick("catalogTextTypes")
	if err != nil {
		return schemaCatalogs{}, err
	}
	file, err := pick("catalogFileTypes")
	if err != nil {
		return schemaCatalogs{}, err
	}
	return schemaCatalogs{form: form, text: text, file: file}, nil
}

// MIME token grammar — byte-equal to the canonical schema's anyOf
// patterns in $defs.contentType. Lowercase-only so the classifier
// refuses to speak before the schema's contentType acceptance has
// passed. A mixed-case value like `Application/vnd.acme+json` must
// surface only as a contentType rejection, never as a class-invariant
// error.
var (
	familyImageAudioVideoRe = regexp.MustCompile(`^(image|audio|video)/[a-z0-9!#$&^_.+-]{1,127}$`)
	familyTextRe            = regexp.MustCompile(`^text/[a-z0-9!#$&^_.+-]{1,127}$`)
	// Subtype first char allows [a-z0-9] per RFC 6838 §4.2 so digit-led
	// IANA types like `application/3gpp-ims+xml` are accepted.
	suffixJSONRe            = regexp.MustCompile(`^[a-z][a-z0-9!#$&^_.-]{0,126}/[a-z0-9][a-z0-9!#$&^_.-]{0,126}\+json$`)
	suffixXMLRe             = regexp.MustCompile(`^[a-z][a-z0-9!#$&^_.-]{0,126}/[a-z0-9][a-z0-9!#$&^_.-]{0,126}\+xml$`)
	suffixZipGzipRe         = regexp.MustCompile(`^[a-z][a-z0-9!#$&^_.-]{0,126}/[a-z0-9][a-z0-9!#$&^_.-]{0,126}\+(zip|gzip)$`)
)

// classifyContentType returns the transport class for v using the same
// first-match-wins rules as the canonical schema and backend Zod (see
// "Classification rule"):
//
//  1. catalogFileTypes
//  2. catalogTextTypes
//  3. catalogFormTypes
//  4. image|audio|video/*  -> file
//  5. text/*               -> text
//  6. */*+json             -> form
//  7. */*+xml              -> text
//  8. */*+(zip|gzip)       -> file
//
// Returns transportUnknown if v matches no rule (caller should not emit
// a class-keyed friendly message in that case — the schema's contentType
// guidance covers it).
func classifyContentType(v string, c schemaCatalogs) transportClass {
	if _, ok := c.file[v]; ok {
		return transportFile
	}
	if _, ok := c.text[v]; ok {
		return transportText
	}
	if _, ok := c.form[v]; ok {
		return transportForm
	}
	if familyImageAudioVideoRe.MatchString(v) {
		return transportFile
	}
	if familyTextRe.MatchString(v) {
		return transportText
	}
	if suffixJSONRe.MatchString(v) {
		return transportForm
	}
	if suffixXMLRe.MatchString(v) {
		return transportText
	}
	if suffixZipGzipRe.MatchString(v) {
		return transportFile
	}
	return transportUnknown
}

// auditInputInvariants walks card.io.inputs[] and produces friendly,
// class-keyed error messages mirroring the backend Zod messages so the
// CLI surface matches the registration-time error text. It returns the
// messages alongside a per-input set of the specific field names the
// audit covered (e.g. {"schema", "example"}). The caller uses that set
// to suppress raw class-machinery duplicates at the exact field path
// without hiding unrelated deeper failures such as an invalid
// `schema.properties[x].type`.
//
// Only the 8 invariant cases enumerated in the transport-class case set are handled;
// anything else falls through to the raw schema messages.
func auditInputInvariants(card map[string]interface{}) ([]string, map[int]map[string]struct{}) {
	cats, err := catalogs()
	if err != nil {
		return nil, nil
	}
	io, _ := card["io"].(map[string]interface{})
	if io == nil {
		return nil, nil
	}
	inputs, _ := io["inputs"].([]interface{})
	if len(inputs) == 0 {
		return nil, nil
	}

	var msgs []string
	handled := map[int]map[string]struct{}{}
	add := func(i int, field, message string) {
		msgs = append(msgs, friendly(i, field, message))
		set, ok := handled[i]
		if !ok {
			set = map[string]struct{}{}
			handled[i] = set
		}
		set[field] = struct{}{}
	}

	for i, raw := range inputs {
		in, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		ct, ok := in["contentType"].(string)
		if !ok || ct == "" {
			continue
		}
		cls := classifyContentType(ct, cats)
		if cls == transportUnknown {
			continue
		}
		_, hasSchema := in["schema"]
		_, hasExample := in["example"]
		_, hasAccept := in["accept"]
		_, hasMaxSize := in["maxSizeBytes"]

		switch cls {
		case transportForm:
			if !hasSchema {
				add(i, "schema", "form-class inputs must declare schema")
			}
			if !hasExample {
				add(i, "example", "form-class inputs must declare example")
			}
			if hasAccept {
				add(i, "accept", "accept is only valid on file-class inputs")
			}
			if hasMaxSize {
				add(i, "maxSizeBytes", "maxSizeBytes is only valid on file-class inputs")
			}
		case transportText:
			if hasSchema {
				add(i, "schema", "schema is only valid on form-class inputs")
			}
			if hasAccept {
				add(i, "accept", "accept is only valid on file-class inputs")
			}
			if hasMaxSize {
				add(i, "maxSizeBytes", "maxSizeBytes is only valid on file-class inputs")
			}
		case transportFile:
			if hasSchema {
				add(i, "schema", "schema is only valid on form-class inputs")
			}
		}
	}
	return msgs, handled
}

func friendly(i int, field, message string) string {
	return fmt.Sprintf("Schema: /io/inputs/%d/%s — %s", i, field, message)
}

// inputFieldPathRe matches raw schema error locations at exactly one of
// the four class-keyword fields of an io.inputs[] item. Deeper paths such
// as /io/inputs/N/schema/properties/... are NOT matched — those carry
// unrelated validation failures that the audit never covered.
var inputFieldPathRe = regexp.MustCompile(`^/io/inputs/(\d+)/(schema|example|accept|maxSizeBytes)$`)

// inputParentPathRe matches raw schema errors at the bare parent path
// /io/inputs/N. These can carry either unrelated required-field failures
// (e.g. missing `description`, which must survive) OR class-covered
// noise (e.g. `missing properties: 'schema', 'example'` that duplicates
// our friendly messages). Suppression here is content-aware — see
// shouldSuppressRaw.
var inputParentPathRe = regexp.MustCompile(`^/io/inputs/(\d+)$`)

// missingPropertiesRe extracts the property list from a parent-node
// `required` failure. Matches singular `missing property: 'X'` and
// plural `missing properties: 'X', 'Y'` shapes emitted by the JSON
// Schema validator.
var missingPropertiesRe = regexp.MustCompile(`^missing propert(?:y|ies): `)
var quotedPropertyRe = regexp.MustCompile(`'([^']+)'`)

// shouldSuppressRaw reports whether a raw schema error should be dropped
// because the audit already produced an equivalent friendly message. Two
// cases:
//
//   - exact field path `/io/inputs/N/<field>` where <field> was covered
//     by the audit, or
//   - parent path `/io/inputs/N` whose message is `missing properties:
//     'X', 'Y'` AND every listed property is covered by the audit
//     (otherwise an unrelated missing field like `description` would
//     be lost).
//
// Deeper sub-paths and parent-path messages that aren't missing-properties
// noise always flow through.
func shouldSuppressRaw(loc, msg string, handled map[int]map[string]struct{}) bool {
	if len(handled) == 0 {
		return false
	}
	if m := inputFieldPathRe.FindStringSubmatch(loc); m != nil {
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			return false
		}
		fields, ok := handled[idx]
		if !ok {
			return false
		}
		_, covered := fields[m[2]]
		return covered
	}
	if m := inputParentPathRe.FindStringSubmatch(loc); m != nil {
		if !missingPropertiesRe.MatchString(msg) {
			return false
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			return false
		}
		fields, ok := handled[idx]
		if !ok {
			return false
		}
		hits := quotedPropertyRe.FindAllStringSubmatch(msg, -1)
		if len(hits) == 0 {
			return false
		}
		for _, h := range hits {
			if _, covered := fields[h[1]]; !covered {
				return false
			}
		}
		return true
	}
	return false
}
