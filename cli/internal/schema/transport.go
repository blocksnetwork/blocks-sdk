package schema

// TransportClass is the public, stable form of the canonical
// agent-card contentType three-way classification (file / text / form).
//
// Other CLI packages — the embed scaffold generator in particular — call
// ClassifyContentType to pick wire shapes per the SDK contract
// without enumerating MIME types themselves. The classifier rules and
// catalogs are owned by friendly.go's classifyContentType; this file is
// only a thin export shim.
type TransportClass string

const (
	// TransportForm covers `application/json`, `application/yaml`, and
	// any `*+json` suffix family. Wire shape: `text: JSON.stringify(value)`.
	TransportForm TransportClass = "form"
	// TransportText covers `text/*`, `application/xml`, `*+xml` suffixes.
	// Wire shape: raw `text:` string.
	TransportText TransportClass = "text"
	// TransportFile covers `application/octet-stream`, `image/*`, `audio/*`,
	// `video/*`, archive families, PDFs, and the `*+zip` / `*+gzip` suffixes.
	// Wire shape: SDK `filePart()` helper.
	TransportFile TransportClass = "file"
	// TransportUnknown is returned for unrecognized contentTypes. Callers
	// MUST treat this as "unknown" and surface a TODO; it is NEVER a
	// silent fallback to one of the three known classes.
	TransportUnknown TransportClass = ""
)

// ClassifyContentType returns the transport class for v using the same
// first-match-wins rules as the canonical agent-card schema and the
// backend Zod validator. Empty / unparseable inputs return TransportUnknown.
//
// Thin export of the package-internal classifyContentType so the embed
// scaffold generator (and any future caller) can reuse the canonical
// classifier without re-loading the schema or enumerating MIME types.
func ClassifyContentType(v string) TransportClass {
	if v == "" {
		return TransportUnknown
	}
	cats, err := catalogs()
	if err != nil {
		return TransportUnknown
	}
	switch classifyContentType(v, cats) {
	case transportForm:
		return TransportForm
	case transportText:
		return TransportText
	case transportFile:
		return TransportFile
	}
	return TransportUnknown
}
