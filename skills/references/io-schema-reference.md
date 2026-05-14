# IO Schema Reference (agent-card.json)

The `io` section of `agent-card.json` defines the inputs and outputs for your
agent. The dashboard uses `schema` to render input forms dynamically. **Without
a proper schema, the agent will not receive correct input.**

## Rules

Each `io.inputs[]` and `io.outputs[]` `contentType` must be accepted by the
canonical schema's four-rule `anyOf` predicate (catalog match, `text|image|audio|video/*`
family wildcard, `*/*+(json|xml|zip|gzip)` suffix, or `application/octet-stream`)
in lowercase canonical form with no `;` parameters.

Every accepted `contentType` belongs to one of three **transport classes**,
which determines what other fields are allowed on the input:

- **form-class inputs** (e.g., `application/json`, `application/ld+json`,
  any `*/*+json` vendor type) — `schema` and `example` are **required**.
  The backend rejects a card missing either with a Zod issue of shape
  `{ code: 'custom', message: 'form-class inputs must declare schema' }` /
  `{ code: 'custom', message: 'form-class inputs must declare example' }`.
  `schema.type` must be `"object"` with a `properties` map; nested
  `properties[*].type` is restricted to the seven JSON-Schema-standard
  types (`string`, `number`, `integer`, `boolean`, `object`, `array`,
  `null`). `accept` and `maxSizeBytes` are forbidden.
- **text-class inputs** (e.g., `text/plain`, `text/markdown`,
  `application/xml`, `application/x-yaml`) — `schema`, `accept`, and
  `maxSizeBytes` are all forbidden. The Playground renders a textarea
  and the wire shape is the raw string.
- **file-class inputs** (e.g., `application/pdf`, `image/png`,
  `model/gltf+json` (catalog override), `application/octet-stream`) —
  `schema` is forbidden; optional `accept` (array of `contentType` or
  bare family glob entries) and `maxSizeBytes` (integer in
  `[1, 26214400]` = 25 MB ceiling) are permitted.

`io.inputs[].description` is **required** on every input.
`io.outputs[].description` remains optional. Outputs have no per-class
invariants — they are descriptive only and may include `schema` /
`example` for any `contentType` to help consumers render results.

Match `schema.properties` (form-class) or your top-level `example`
(text-class) to the fields your handler reads from
`task.requestParts[0]` and the shape of the object you return.

## Default Values

Closes documentation gap #469 — how to declare a default value for an
input field so the Playground UI can pre-fill the form.

The mechanism depends on the input's transport class:

### form-class inputs (JSON forms)

Declare `default` inside each property of the `schema.properties` map.
The Playground reads `schema.properties[*].default` and pre-fills the
matching field of the rendered JSON form. Example:

```json
{
  "id": "request",
  "description": "Text to process",
  "contentType": "application/json",
  "required": true,
  "example": { "text": "Hello from the Blocks Network!" },
  "schema": {
    "type": "object",
    "required": ["text"],
    "properties": {
      "text": {
        "type": "string",
        "title": "Input Text",
        "default": "Hello from the Blocks Network!"
      }
    }
  }
}
```

The Single Text Input, Structured Multi-Field Input, and Boolean and
Enum Fields examples below all use `default`.

### text-class inputs (textareas)

Use the top-level `example` field on the input entry. The Playground
pre-fills the textarea with the value of `example` (which must be a
string for text-class inputs). `schema` is forbidden on text-class
inputs, so `schema.properties[*].default` does not apply. Example:

```json
{
  "id": "prompt",
  "description": "Markdown prompt for the model",
  "contentType": "text/markdown",
  "required": true,
  "example": "Summarize the following text in three bullet points:\n\n..."
}
```

### file-class inputs (file pickers)

No default is supported — file pickers cannot be pre-filled with file
bytes. Declare `accept` to constrain file types and `maxSizeBytes` to
hint at the per-input upload ceiling, but the user must always select
a file at task-submit time.

Each `schema` follows JSON Schema format and **must** include `type:
"object"` and a `properties` map for form-class inputs. Each property
needs at least `type` and `title`.

---

## Example: Single Text Input

```json
"io": {
  "inputs": [
    {
      "id": "request",
      "description": "Text to process",
      "contentType": "application/json",
      "required": true,
      "example": { "text": "Hello from the Blocks Network!" },
      "schema": {
        "type": "object",
        "required": ["text"],
        "properties": {
          "text": {
            "type": "string",
            "title": "Input Text",
            "default": "Hello from the Blocks Network!"
          }
        }
      }
    }
  ],
  "outputs": [
    {
      "id": "result",
      "description": "Processed result",
      "contentType": "text/plain",
      "guaranteed": true
    }
  ]
}
```

## Example: Structured Multi-Field Input

```json
"io": {
  "inputs": [
    {
      "id": "numbers",
      "description": "Two numbers to add",
      "contentType": "application/json",
      "required": true,
      "example": { "a": 5, "b": 3 },
      "schema": {
        "type": "object",
        "required": ["a", "b"],
        "properties": {
          "a": { "type": "number", "title": "Number A", "default": 5 },
          "b": { "type": "number", "title": "Number B", "default": 3 }
        }
      }
    }
  ],
  "outputs": [
    {
      "id": "sum",
      "description": "Sum result",
      "contentType": "application/json",
      "guaranteed": true,
      "example": { "a": 5, "b": 3, "sum": 8 },
      "schema": {
        "type": "object",
        "properties": {
          "a": { "type": "number" },
          "b": { "type": "number" },
          "sum": { "type": "number" }
        }
      }
    }
  ]
}
```

## Example: Boolean and Enum Fields

```json
"schema": {
  "type": "object",
  "required": ["query"],
  "properties": {
    "query": { "type": "string", "title": "Search Query" },
    "verbose": { "type": "boolean", "title": "Verbose Output", "default": false },
    "format": { "type": "string", "title": "Output Format", "enum": ["json", "text", "csv"], "default": "json" }
  }
}
```

## Example: Array Field

```json
"schema": {
  "type": "object",
  "required": ["items"],
  "properties": {
    "items": {
      "type": "array",
      "title": "Items",
      "items": { "type": "string" }
    }
  }
}
```
