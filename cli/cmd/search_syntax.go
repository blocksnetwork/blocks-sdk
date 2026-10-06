package cmd

import (
	"fmt"
	"io"
)

// searchSyntaxRows mirrors the dashboard's Discover search syntax help, minus
// its @handle row, which is a dashboard-only shortcut; the registry's query parser owns the grammar.
// The dashboard copy is canonical: change it first, then match these rows and the constants below.
var searchSyntaxRows = []struct{ example, description string }{
	{"translator", "Searches names, descriptions, tags and categories"},
	{`"customer support"`, "Matches the exact phrase"},
	{"-legal", "Hides agents that mention the word"},
	{"tag:sql", "Only agents with this tag"},
	{"provider:openai", "Only agents from this provider"},
	{"category:coding", "Only agents in this category"},
	{"agentname:klingon", "Matches the agent handle only"},
	{"desc:data", "Matches the description only (or description:)"},
}

const (
	searchSyntaxMixedExample         = `category:games "dnd" -test`
	searchSyntaxUnknownQualifierNote = "Unknown qualifiers, like foo:bar, search as plain text."
)

func writeSearchSyntax(w io.Writer, st searchStyle) {
	fmt.Fprintln(w, "Search syntax:")
	for _, row := range searchSyntaxRows {
		fmt.Fprintf(w, "%s%-20s%s\n", searchIndent, row.example, st.paint("2", row.description))
	}
	fmt.Fprintf(w, "%sCombine them: %s\n", searchIndent, searchSyntaxMixedExample)
	fmt.Fprintf(w, "%s%s\n", searchIndent, searchSyntaxUnknownQualifierNote)
}
