package auth

import (
	"strings"
	"testing"
)

func TestBuildApiKeyName(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		want     string
	}{
		{"empty hostname", "", "cli-default"},
		{"whitespace only", "   ", "cli-default"},
		{"short hostname", "x", "cli-x"},
		{"macos .local stripped", "Mateuszs-MacBook-Pro.local", "cli-Mateuszs-MacBook-Pro"},
		{"spaces replaced", "My Laptop", "cli-My-Laptop"},
		{"unicode replaced", "héllo", "cli-h-llo"},
		{"dash collapse", "a--b___c", "cli-a-b___c"},
		{"trim leading/trailing dashes", "--weird--", "cli-weird"},
		{
			"very long truncated",
			strings.Repeat("a", apiKeyNameMaxLen+10),
			"cli-" + strings.Repeat("a", apiKeyNameMaxLen-len(apiKeyNamePrefix)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildApiKeyName(tt.hostname)
			if got != tt.want {
				t.Errorf("BuildApiKeyName(%q) = %q, want %q", tt.hostname, got, tt.want)
			}
			if len(got) > apiKeyNameMaxLen {
				t.Errorf("result %q exceeds max length %d", got, apiKeyNameMaxLen)
			}
			if len(got) < 1 {
				t.Errorf("result is empty")
			}
		})
	}
}

func TestBuildApiKeyName_NeverEndsWithDashAfterTruncate(t *testing.T) {
	// Construct a hostname where naive truncation would leave a trailing dash.
	// "cli-" (4) + "a"*250 + "-" + "b"*10 -> truncated at 255 ends with '-'.
	host := strings.Repeat("a", 250) + "-" + strings.Repeat("b", 10)
	got := BuildApiKeyName(host)
	if strings.HasSuffix(got, "-") || strings.HasSuffix(got, ".") {
		t.Errorf("result %q should not end with '-' or '.'", got)
	}
}
