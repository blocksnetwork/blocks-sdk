// Package agentname owns the agent-name grammar and suggests a valid name for an invalid one.
package agentname

import (
	"fmt"
	"regexp"
	"strings"
)

var Pattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

var separatorRun = regexp.MustCompile(`[^A-Za-z0-9]+`)

// Suggest applies the name normalization in skills/blocks-getstarted/SKILL.md §Step 1: Ask Name.
func Suggest(name string) string {
	return strings.Trim(separatorRun.ReplaceAllString(name, "_"), "_")
}

func Hint(name string) string {
	if s := Suggest(name); s != "" {
		return fmt.Sprintf(" — try %q", s)
	}
	return ""
}
