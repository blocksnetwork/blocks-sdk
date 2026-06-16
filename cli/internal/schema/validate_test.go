package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCard(t *testing.T, dir string, card map[string]interface{}) string {
	t.Helper()
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-card.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func validCard() map[string]interface{} {
	return map[string]interface{}{
		"identity": map[string]interface{}{
			"agentName":   "test_agent",
			"displayName": "test_agent",
			"description": "A test agent",
			"version":     "1.0.0",
			"provider":    map[string]interface{}{"organization": "TestOrg"},
		},
		"capabilities": map[string]interface{}{
			"taskKinds": []interface{}{"request"},
		},
		"tags": []interface{}{
			map[string]interface{}{
				"id":   "main",
				"name": "Main",
			},
		},
		"runtime": map[string]interface{}{
			"handler": "./handler.py",
		},
	}
}

func TestValidateFileNotFound(t *testing.T) {
	res := Validate("/tmp/nonexistent-path-for-test/agent-card.json")
	if len(res.Errors) == 0 {
		t.Fatal("expected errors for non-existent file")
	}
	if !strings.Contains(res.Errors[0], "not found") {
		t.Errorf("expected 'not found' error, got: %s", res.Errors[0])
	}
}

func TestValidateInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-card.json")
	if err := os.WriteFile(path, []byte("{invalid json}"), 0644); err != nil {
		t.Fatal(err)
	}

	res := Validate(path)
	if len(res.Errors) == 0 {
		t.Fatal("expected errors for invalid JSON")
	}
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "Invalid JSON") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Invalid JSON' error, got: %v", res.Errors)
	}
}

func TestValidateValidCard(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	card := validCard()
	path := writeCard(t, dir, card)

	res := Validate(path)
	if len(res.Errors) > 0 {
		t.Errorf("expected no errors, got: %v", res.Errors)
	}
	if len(res.Successes) < 5 {
		t.Errorf("expected at least 5 successes, got %d: %v", len(res.Successes), res.Successes)
	}
}

func TestValidateMissingRequiredFields(t *testing.T) {
	requiredFields := []string{"identity", "capabilities", "tags", "runtime"}

	for _, field := range requiredFields {
		t.Run(field, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
				t.Fatal(err)
			}

			card := validCard()
			delete(card, field)
			path := writeCard(t, dir, card)

			res := Validate(path)
			if len(res.Errors) == 0 {
				t.Errorf("expected errors when %q is missing", field)
			}
		})
	}
}

func TestValidateMissingRuntime(t *testing.T) {
	dir := t.TempDir()
	card := validCard()
	delete(card, "runtime")
	path := writeCard(t, dir, card)

	res := Validate(path)
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "Missing runtime section") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Missing runtime section' error, got: %v", res.Errors)
	}
}

func TestValidateMissingHandler(t *testing.T) {
	dir := t.TempDir()
	card := validCard()
	// Do not create handler.py
	path := writeCard(t, dir, card)

	res := Validate(path)
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "Handler not found") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Handler not found' error, got: %v", res.Errors)
	}
}

func TestValidateMissingAgentName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	card := validCard()
	id := card["identity"].(map[string]interface{})
	delete(id, "agentName")
	path := writeCard(t, dir, card)

	res := Validate(path)
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "agentName") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected agentName error, got: %v", res.Errors)
	}
}

func TestValidateInvalidAgentNameFormat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	card := validCard()
	id := card["identity"].(map[string]interface{})
	id["agentName"] = "acme.echo"
	path := writeCard(t, dir, card)

	res := Validate(path)
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "alphanumeric") || strings.Contains(e, "pattern") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected agentName format error, got: %v", res.Errors)
	}
}

func TestValidateAdditionalProperties(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	card := validCard()
	card["unknownField"] = "should fail"
	path := writeCard(t, dir, card)

	res := Validate(path)
	if len(res.Errors) == 0 {
		t.Error("expected errors for additional properties")
	}
}

func TestValidateOldFormatCardRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	// Old-format card with top-level name/description/version and capabilities.streaming
	oldCard := map[string]interface{}{
		"name":               "test_agent",
		"description":        "A test agent",
		"version":            "1.0.0",
		"provider":           map[string]interface{}{"organization": "TestOrg"},
		"defaultInputModes":  []string{"application/json"},
		"defaultOutputModes": []string{"application/json"},
		"capabilities":       map[string]interface{}{"streaming": false},
		"tags": []interface{}{
			map[string]interface{}{"id": "main", "name": "Main"},
		},
		"runtime": map[string]interface{}{
			"agentName": "test_agent",
			"handler":   "./handler.py",
		},
	}
	path := writeCard(t, dir, oldCard)

	res := Validate(path)
	if len(res.Errors) == 0 {
		t.Error("old-format card should fail validation against new schema")
	}
}

func TestValidateDuplicateInputIDs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	card := validCard()
	card["io"] = map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{"id": "text", "description": "first", "contentType": "text/plain", "required": true},
			map[string]interface{}{"id": "text", "description": "dup", "contentType": "text/plain", "required": false},
		},
		"outputs": []interface{}{
			map[string]interface{}{"id": "result", "contentType": "text/plain", "guaranteed": true},
		},
	}
	path := writeCard(t, dir, card)

	res := Validate(path)
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "Duplicate id") && strings.Contains(e, "io.inputs") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected duplicate input ID error, got: %v", res.Errors)
	}
}

func TestValidateDuplicateOutputIDs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	card := validCard()
	card["io"] = map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{"id": "text", "description": "in", "contentType": "text/plain", "required": true},
		},
		"outputs": []interface{}{
			map[string]interface{}{"id": "result", "contentType": "text/plain", "guaranteed": true},
			map[string]interface{}{"id": "result", "contentType": "application/json", "guaranteed": false},
		},
	}
	path := writeCard(t, dir, card)

	res := Validate(path)
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "Duplicate id") && strings.Contains(e, "io.outputs") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected duplicate output ID error, got: %v", res.Errors)
	}
}

func TestValidateDuplicateTagIDs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	card := validCard()
	card["tags"] = []interface{}{
		map[string]interface{}{"id": "main", "name": "Main"},
		map[string]interface{}{"id": "main", "name": "Duplicate"},
	}
	path := writeCard(t, dir, card)

	res := Validate(path)
	found := false
	for _, e := range res.Errors {
		if strings.Contains(e, "Duplicate id") && strings.Contains(e, "tags") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected duplicate tag ID error, got: %v", res.Errors)
	}
}

func TestValidateUniqueIDsPass(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}

	card := validCard()
	card["io"] = map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{"id": "text", "description": "in", "contentType": "text/plain", "required": true},
			map[string]interface{}{
				"id":          "config",
				"description": "cfg",
				"contentType": "application/json",
				"required":    false,
				"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
				"example":     map[string]interface{}{},
			},
		},
		"outputs": []interface{}{
			map[string]interface{}{"id": "result", "contentType": "text/plain", "guaranteed": true},
			map[string]interface{}{"id": "export", "contentType": "text/csv", "guaranteed": false},
		},
	}
	path := writeCard(t, dir, card)

	res := Validate(path)
	if len(res.Errors) > 0 {
		t.Errorf("expected no errors on a fully-valid io block, got: %v", res.Errors)
	}
	foundIDSuccess := false
	for _, s := range res.Successes {
		if strings.Contains(s, "IDs are unique") {
			foundIDSuccess = true
			break
		}
	}
	if !foundIDSuccess {
		t.Errorf("expected ID uniqueness success message, got successes: %v, errors: %v", res.Successes, res.Errors)
	}
}

// ---------------------------------------------------------------------------
// contentType four-rule acceptance / rejection / class invariants
// (mirrors tests/schemas/agent-card-contentType.test.ts).
// ---------------------------------------------------------------------------

func cardWithInput(t *testing.T, dir string, input map[string]interface{}) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}
	c := validCard()
	c["io"] = map[string]interface{}{
		"inputs": []interface{}{input},
	}
	return writeCard(t, dir, c)
}

func cardWithOutput(t *testing.T, dir string, output map[string]interface{}) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}
	c := validCard()
	c["io"] = map[string]interface{}{
		"outputs": []interface{}{output},
	}
	return writeCard(t, dir, c)
}

func hasSchemaError(errs []string) bool {
	for _, e := range errs {
		if strings.HasPrefix(e, "Schema:") {
			return true
		}
	}
	return false
}

func TestValidateContentTypeAcceptedFamily(t *testing.T) {
	cases := []string{
		"image/jxl",
		"audio/opus",
		"text/x-kotlin",
		"video/quicktime",
	}
	for _, ct := range cases {
		t.Run(ct, func(t *testing.T) {
			path := cardWithInput(t, t.TempDir(), map[string]interface{}{
				"id":          "in1",
				"description": "d",
				"contentType": ct,
				"required":    true,
			})
			res := Validate(path)
			if hasSchemaError(res.Errors) {
				t.Errorf("expected schema acceptance for %s, got: %v", ct, res.Errors)
			}
		})
	}
}

func TestValidateContentTypeAcceptedSuffix(t *testing.T) {
	// +json => form class, requires schema+example.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/vnd.acme.invoice+json",
		"required":    true,
		"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		"example":     map[string]interface{}{},
	})
	res := Validate(path)
	if hasSchemaError(res.Errors) {
		t.Errorf("expected acceptance for vendor +json suffix, got: %v", res.Errors)
	}

	// +xml => text class.
	path2 := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/vnd.acme.report+xml",
		"required":    true,
	})
	res2 := Validate(path2)
	if hasSchemaError(res2.Errors) {
		t.Errorf("expected acceptance for vendor +xml suffix, got: %v", res2.Errors)
	}

	// +zip and +gzip => file class.
	for _, ct := range []string{
		"application/vnd.novel-ebook+zip",
		"application/vnd.gzipped-data+gzip",
	} {
		t.Run(ct, func(t *testing.T) {
			path := cardWithInput(t, t.TempDir(), map[string]interface{}{
				"id":          "in1",
				"description": "d",
				"contentType": ct,
				"required":    true,
			})
			res := Validate(path)
			if hasSchemaError(res.Errors) {
				t.Errorf("expected acceptance for %s, got: %v", ct, res.Errors)
			}
		})
	}
}

func TestValidateContentTypeRejected(t *testing.T) {
	cases := []string{
		"applicaton/json",
		"imag/png",
		"Application/JSON",
		"Image/PNG",
		"application/json; charset=utf-8",
		"application/vnd.acme.invoice",
		"multipart/form-data",
		"message/rfc822",
	}
	for _, ct := range cases {
		t.Run(ct, func(t *testing.T) {
			path := cardWithInput(t, t.TempDir(), map[string]interface{}{
				"id":          "in1",
				"description": "d",
				"contentType": ct,
				"required":    true,
			})
			res := Validate(path)
			if !hasSchemaError(res.Errors) {
				t.Errorf("expected schema rejection for %s, got successes: %v", ct, res.Successes)
			}
			// Friendly guidance text is appended to the leaf error.
			foundGuidance := false
			for _, e := range res.Errors {
				if strings.Contains(e, "/contentType") &&
					strings.Contains(e, "Blocks supported catalog") {
					foundGuidance = true
					break
				}
			}
			if !foundGuidance {
				t.Errorf("expected friendly contentType guidance, got: %v", res.Errors)
			}
		})
	}
}

func TestValidateFormClassRequiresSchemaAndExample(t *testing.T) {
	// Missing schema.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/json",
		"required":    true,
		"example":     map[string]interface{}{},
	})
	res := Validate(path)
	if !hasSchemaError(res.Errors) {
		t.Error("expected schema error for form-class input missing schema")
	}

	// Missing example.
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/json",
		"required":    true,
		"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
	})
	res = Validate(path)
	if !hasSchemaError(res.Errors) {
		t.Error("expected schema error for form-class input missing example")
	}

	// Both present passes.
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/json",
		"required":    true,
		"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		"example":     map[string]interface{}{},
	})
	res = Validate(path)
	if hasSchemaError(res.Errors) {
		t.Errorf("expected acceptance, got: %v", res.Errors)
	}
}

func TestValidateClassOverrides(t *testing.T) {
	// model/gltf+json => file class via catalog override.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "model/gltf+json",
		"required":    true,
	})
	if hasSchemaError(Validate(path).Errors) {
		t.Error("model/gltf+json should be accepted as file class without schema")
	}
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "model/gltf+json",
		"required":    true,
		"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
	})
	if !hasSchemaError(Validate(path).Errors) {
		t.Error("schema is forbidden on file-class model/gltf+json")
	}

	// application/xml => text class via catalog override.
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/xml",
		"required":    true,
	})
	if hasSchemaError(Validate(path).Errors) {
		t.Error("application/xml should be accepted as text class")
	}
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/xml",
		"required":    true,
		"accept":      []interface{}{"text/*"},
	})
	if !hasSchemaError(Validate(path).Errors) {
		t.Error("accept is forbidden on text-class application/xml")
	}

	// application/gpx+xml => file class via catalog override.
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/gpx+xml",
		"required":    true,
		"maxSizeBytes": 1024,
	})
	if hasSchemaError(Validate(path).Errors) {
		t.Error("application/gpx+xml should accept maxSizeBytes as file class")
	}

	// application/vnd.google-earth.kml+xml => text class catalog inclusion.
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/vnd.google-earth.kml+xml",
		"required":    true,
	})
	if hasSchemaError(Validate(path).Errors) {
		t.Error("application/vnd.google-earth.kml+xml should be accepted as text class")
	}
}

func TestValidateInputDescriptionRequired(t *testing.T) {
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"contentType": "text/plain",
		"required":    true,
	})
	res := Validate(path)
	if !hasSchemaError(res.Errors) {
		t.Error("input missing description should fail schema validation")
	}
}

func TestValidateOutputDescriptionOptional(t *testing.T) {
	path := cardWithOutput(t, t.TempDir(), map[string]interface{}{
		"id":          "out1",
		"contentType": "text/plain",
		"guaranteed":  true,
	})
	res := Validate(path)
	if hasSchemaError(res.Errors) {
		t.Errorf("output without description should pass, got: %v", res.Errors)
	}
}

func TestValidateAcceptItemValidation(t *testing.T) {
	// Family glob valid.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "image/png",
		"required":    true,
		"accept":      []interface{}{"image/*", "application/pdf"},
	})
	if hasSchemaError(Validate(path).Errors) {
		t.Errorf("accept with valid family glob should pass, got: %v", Validate(path).Errors)
	}

	// Uppercase rejected with friendly guidance.
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "image/png",
		"required":    true,
		"accept":      []interface{}{"Image/*"},
	})
	res := Validate(path)
	if !hasSchemaError(res.Errors) {
		t.Error("uppercase accept item should be rejected")
	}
	foundGuidance := false
	for _, e := range res.Errors {
		if strings.Contains(e, "/accept/") && strings.Contains(e, "Blocks supported catalog") {
			foundGuidance = true
			break
		}
	}
	if !foundGuidance {
		t.Errorf("expected friendly guidance on accept item rejection, got: %v", res.Errors)
	}
}

func TestValidateMaxSizeBytesBounds(t *testing.T) {
	// 25 MB exact ok.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":           "in1",
		"description":  "d",
		"contentType":  "image/png",
		"required":     true,
		"maxSizeBytes": 26214400,
	})
	if hasSchemaError(Validate(path).Errors) {
		t.Error("maxSizeBytes=26214400 should pass")
	}

	// One over rejected.
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":           "in1",
		"description":  "d",
		"contentType":  "image/png",
		"required":     true,
		"maxSizeBytes": 26214401,
	})
	if !hasSchemaError(Validate(path).Errors) {
		t.Error("maxSizeBytes=26214401 should be rejected")
	}
}

func TestValidateNestedSchemaPropertyType(t *testing.T) {
	// Non-standard nested type rejected.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/json",
		"required":    true,
		"example":     map[string]interface{}{},
		"schema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"when": map[string]interface{}{"type": "date"},
			},
		},
	})
	if !hasSchemaError(Validate(path).Errors) {
		t.Error("non-standard nested type should be rejected")
	}

	// All seven standard types accepted, including null.
	path = cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/json",
		"required":    true,
		"example":     map[string]interface{}{},
		"schema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"s": map[string]interface{}{"type": "string"},
				"n": map[string]interface{}{"type": "number"},
				"i": map[string]interface{}{"type": "integer"},
				"b": map[string]interface{}{"type": "boolean"},
				"o": map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{"x": map[string]interface{}{"type": "string"}},
				},
				"a": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
				},
				"z": map[string]interface{}{"type": "null"},
			},
		},
	})
	if hasSchemaError(Validate(path).Errors) {
		t.Errorf("standard nested types should pass, got: %v", Validate(path).Errors)
	}
}

// ---------------------------------------------------------------------------
// Transport-class classifier (mirrors backend Zod classifyContentType + the
// canonical schema's $defs.catalog*Types enums + first-match-wins rules).
// ---------------------------------------------------------------------------

func TestClassifyContentType(t *testing.T) {
	cats, err := catalogs()
	if err != nil {
		t.Fatalf("loadSchemaCatalogs failed: %v", err)
	}
	cases := []struct {
		ct   string
		want transportClass
	}{
		// Catalog overrides win over family/suffix defaults.
		{"model/gltf+json", transportFile},                   // file catalog beats +json suffix
		{"application/gpx+xml", transportFile},               // file catalog beats +xml suffix
		{"application/vnd.google-earth.kml+xml", transportText},
		{"application/xml", transportText},                   // text catalog beats application/* default
		{"application/json", transportForm},                  // form catalog
		// Family wildcards.
		{"image/png", transportFile},
		{"audio/opus", transportFile},
		{"video/mp4", transportFile},
		{"text/plain", transportText},
		// Suffix patterns (uncataloged).
		{"application/vnd.acme.report+json", transportForm},
		{"application/vnd.acme.report+xml", transportText},
		{"application/vnd.acme.archive+zip", transportFile},
		{"application/vnd.acme.bundle+gzip", transportFile},
		// No-match: caller should leave class-keyed translation alone.
		{"application/vnd.acme.invoice", transportUnknown},
		{"applicaton/json", transportUnknown}, // typo
		// Mixed case is a contentType rejection, not a class.
		{"Application/vnd.acme.report+json", transportUnknown},
		{"Text/plain", transportUnknown},
		{"IMAGE/PNG", transportUnknown},
		// RFC 6838 §4.2 permits digit-led subtype names (lowercase).
		{"application/3gpp-ims+xml", transportText},
		{"application/3gpp-vendor+json", transportForm},
	}
	for _, tc := range cases {
		t.Run(tc.ct, func(t *testing.T) {
			got := classifyContentType(tc.ct, cats)
			if got != tc.want {
				t.Errorf("classify(%q) = %q, want %q", tc.ct, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Friendly transport-class invariant translations
// (mirrors the 8 cases in CLI_FRIENDLY_ERRORS_IMPL.md §3 + IMPL Part 9).
// ---------------------------------------------------------------------------

func findFriendly(t *testing.T, errs []string, want string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e, want) {
			return
		}
	}
	t.Errorf("expected error containing %q, got: %v", want, errs)
}

func TestFriendlyTextClassWithSchema(t *testing.T) {
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/xml",
		"required":    true,
		"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/schema — schema is only valid on form-class inputs")
}

func TestFriendlyTextClassWithAccept(t *testing.T) {
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/xml",
		"required":    true,
		"accept":      []interface{}{"text/plain"},
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/accept — accept is only valid on file-class inputs")
}

func TestFriendlyTextClassWithMaxSizeBytes(t *testing.T) {
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":           "in1",
		"description":  "d",
		"contentType":  "application/xml",
		"required":     true,
		"maxSizeBytes": 1024,
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/maxSizeBytes — maxSizeBytes is only valid on file-class inputs")
}

func TestFriendlyFileClassWithSchema(t *testing.T) {
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "model/gltf+json",
		"required":    true,
		"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/schema — schema is only valid on form-class inputs")
}

func TestFriendlyFormClassWithAccept(t *testing.T) {
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/json",
		"required":    true,
		"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		"example":     map[string]interface{}{},
		"accept":      []interface{}{"application/json"},
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/accept — accept is only valid on file-class inputs")
}

func TestFriendlyFormClassWithMaxSizeBytes(t *testing.T) {
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":           "in1",
		"description":  "d",
		"contentType":  "application/json",
		"required":     true,
		"schema":       map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		"example":      map[string]interface{}{},
		"maxSizeBytes": 1024,
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/maxSizeBytes — maxSizeBytes is only valid on file-class inputs")
}

func TestFriendlyVendorJSONMissingSchema(t *testing.T) {
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/vnd.acme.report+json",
		"required":    true,
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/schema — form-class inputs must declare schema")
	findFriendly(t, res.Errors, "/io/inputs/0/example — form-class inputs must declare example")
}

func TestFriendlySuppressesRawClassNoise(t *testing.T) {
	// text-class input with forbidden schema should produce exactly one
	// friendly line about /io/inputs/0/schema, not two (raw + friendly).
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/xml",
		"required":    true,
		"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
	})
	res := Validate(path)
	count := 0
	for _, e := range res.Errors {
		if strings.Contains(e, "/io/inputs/0/schema") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one /io/inputs/0/schema error, got %d: %v", count, res.Errors)
	}
}

func TestFriendlyMixedCaseContentTypeEmitsOnlyGuidance(t *testing.T) {
	// Reviewer case: a mixed-case contentType is a schema rejection.
	// The classifier must not classify it as form/text/file, or the
	// audit will tack bogus "schema/example missing" errors onto what
	// is already a clean contentType failure.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "Application/vnd.acme.report+json",
		"required":    true,
	})
	res := Validate(path)
	for _, e := range res.Errors {
		if strings.Contains(e, "form-class inputs must declare") ||
			strings.Contains(e, "text-class") ||
			strings.Contains(e, "file-class") {
			t.Errorf("mixed-case contentType should not trigger class-invariant errors, got: %q", e)
		}
	}
	foundGuidance := false
	for _, e := range res.Errors {
		if strings.Contains(e, "/io/inputs/0/contentType") && strings.Contains(e, "Blocks supported catalog") {
			foundGuidance = true
		}
	}
	if !foundGuidance {
		t.Errorf("expected contentType four-rule guidance, got: %v", res.Errors)
	}
}

func TestFriendlyPreservesParentNodeErrors(t *testing.T) {
	// Reviewer case: a form-class input missing `description` AND `schema`
	// AND `example`. The missing-description failure surfaces at the
	// parent path /io/inputs/0; suppressing bare parent-path errors would
	// hide it behind the class-invariant messages.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}
	c := validCard()
	c["io"] = map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{
				"id":          "in1",
				"contentType": "application/json",
				"required":    true,
			},
		},
	}
	path := writeCard(t, dir, c)
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/schema — form-class inputs must declare schema")
	findFriendly(t, res.Errors, "/io/inputs/0/example — form-class inputs must declare example")
	foundDescriptionError := false
	for _, e := range res.Errors {
		if strings.Contains(e, "description") &&
			(strings.Contains(e, "missing") || strings.Contains(e, "required")) {
			foundDescriptionError = true
			break
		}
	}
	if !foundDescriptionError {
		t.Errorf("expected missing-description error to survive, got: %v", res.Errors)
	}
}

func TestFriendlySuppressesClassCoveredMissingProperties(t *testing.T) {
	// A form-class input missing schema AND example produces friendly
	// per-field messages AND a raw parent-path "missing properties:
	// 'schema', 'example'". Both missing props are class-covered, so the
	// parent-path line duplicates the friendly messages and should drop.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/json",
		"required":    true,
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/schema — form-class inputs must declare schema")
	findFriendly(t, res.Errors, "/io/inputs/0/example — form-class inputs must declare example")
	for _, e := range res.Errors {
		if strings.Contains(e, "/io/inputs/0 — missing propert") {
			t.Errorf("class-covered missing-properties line should be suppressed, got: %q", e)
		}
	}
}

func TestFriendlyKeepsMixedMissingProperties(t *testing.T) {
	// Missing description (not class-covered) AND missing schema/example
	// (class-covered) ⇒ the parent-path message listing ALL THREE must
	// survive because description is not audit-covered. The per-field
	// friendly messages still appear for schema and example.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}
	c := validCard()
	c["io"] = map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{
				"id":          "in1",
				"contentType": "application/json",
				"required":    true,
			},
		},
	}
	path := writeCard(t, dir, c)
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/schema — form-class inputs must declare schema")
	findFriendly(t, res.Errors, "/io/inputs/0/example — form-class inputs must declare example")
	foundDescription := false
	for _, e := range res.Errors {
		if strings.Contains(e, "description") &&
			(strings.Contains(e, "missing") || strings.Contains(e, "required")) {
			foundDescription = true
			break
		}
	}
	if !foundDescription {
		t.Errorf("expected missing-description error to survive, got: %v", res.Errors)
	}
}

func TestFriendlyPreservesDeepSchemaErrors(t *testing.T) {
	// Reviewer case: a form-class input that is BOTH missing example AND
	// has an invalid nested schema.properties[*].type must surface both
	// failures. Suppressing every /io/inputs/0/schema* error because the
	// audit caught the missing example would hide the real property-type
	// bug.
	path := cardWithInput(t, t.TempDir(), map[string]interface{}{
		"id":          "in1",
		"description": "d",
		"contentType": "application/json",
		"required":    true,
		"schema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"when": map[string]interface{}{"type": "date"},
			},
		},
		// no example
	})
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/example — form-class inputs must declare example")
	foundDeep := false
	for _, e := range res.Errors {
		if strings.Contains(e, "/io/inputs/0/schema/properties/when") {
			foundDeep = true
			break
		}
	}
	if !foundDeep {
		t.Errorf("expected deep schema.properties[*].type error to survive, got: %v", res.Errors)
	}
}

func TestFriendlyPreservesContentTypeGuidance(t *testing.T) {
	// An invariant-failing input must NOT swallow the contentType
	// four-rule guidance for a *different* input on the same card.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}
	c := validCard()
	c["io"] = map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{
				"id":          "good",
				"description": "d",
				"contentType": "application/xml",
				"required":    true,
				"schema":      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			},
			map[string]interface{}{
				"id":          "typo",
				"description": "d",
				"contentType": "applicaton/json",
				"required":    true,
			},
		},
	}
	path := writeCard(t, dir, c)
	res := Validate(path)
	findFriendly(t, res.Errors, "/io/inputs/0/schema — schema is only valid on form-class inputs")
	foundGuidance := false
	for _, e := range res.Errors {
		if strings.Contains(e, "/io/inputs/1/contentType") && strings.Contains(e, "Blocks supported catalog") {
			foundGuidance = true
		}
	}
	if !foundGuidance {
		t.Errorf("expected contentType guidance on input 1 to survive, got: %v", res.Errors)
	}
}

// TestValidateWebAppURLs exercises the post-schema step 7 added by F1
// follow-up r2: `blocks check` now runs `deploy.ValidateWebAppURL` over
// each identity.webApps[].url so the CLI catches parser-level rejections
// the JSON Schema pattern alone can't express (invalid percent-encoding,
// out-of-range port, malformed IPv6 literal). Without this step a card
// passing `blocks check` could still fail `blocks publish` at the
// backend's Zod refine.
func TestValidateWebAppURLs(t *testing.T) {
	cases := []struct {
		name string
		urls []string
		// expectErr is the substring expected on stderr; "" means valid.
		expectErr string
	}{
		{
			name: "production https accepted",
			urls: []string{"https://my-app.pages.dev", "https://example.com/path?q=1"},
		},
		{
			name: "loopback http accepted",
			urls: []string{"http://localhost:5173", "http://127.0.0.1:8080"},
		},
		{
			name:      "port out of range rejected",
			urls:      []string{"https://example.com:99999"},
			expectErr: "1-65535",
		},
		{
			name:      "invalid percent-encoding rejected",
			urls:      []string{"https://%zz"},
			expectErr: "percent-encoding",
		},
		{
			name:      "malformed IPv6 rejected",
			urls:      []string{"https://[gggg::1]"},
			expectErr: "invalid host",
		},
		{
			name:      "wrong scheme rejected",
			urls:      []string{"http://example.com"},
			expectErr: "loopback",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
				t.Fatal(err)
			}
			card := validCard()
			identity := card["identity"].(map[string]interface{})
			webApps := []interface{}{}
			for _, u := range tc.urls {
				webApps = append(webApps, map[string]interface{}{"url": u})
			}
			identity["webApps"] = webApps
			path := writeCard(t, dir, card)
			res := Validate(path)

			joined := strings.Join(res.Errors, "\n")
			if tc.expectErr == "" {
				if strings.Contains(joined, "identity.webApps[") {
					t.Errorf("unexpected webApps error: %v", res.Errors)
				}
			} else {
				if !strings.Contains(joined, tc.expectErr) {
					t.Errorf("expected webApps error containing %q, got:\n%s", tc.expectErr, joined)
				}
			}
		})
	}
}
