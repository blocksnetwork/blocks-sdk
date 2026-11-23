package connectors

import (
	"fmt"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

func TestErrorClassification(t *testing.T) {
	api := func(status int, code string) error {
		return fmt.Errorf("wrapped: %w", &blocksapi.APIError{StatusCode: status, Code: code})
	}
	cases := []struct {
		name string
		err  error
		fn   func(error) bool
		want bool
	}{
		{"name taken 403", api(403, CodeAgentNameTaken), IsNameTaken, true},
		{"org mismatch 403", api(403, CodeAgentOrgMismatch), IsNameTaken, true},
		{"other conflict", api(409, CodeKeysetDisabled), IsNameTaken, false},
		{"coded 403 is not permission", api(403, CodeExposureOrgNotAllowed), IsPermissionDenied, false},
		{"connection 404 is not feature off", api(404, CodeConnectionNotFound), IsFeatureOff, false},
		{"linked keyset disabled is not already linked", api(409, CodeKeysetDisabled), IsAlreadyLinked, false},
	}
	for _, tc := range cases {
		if got := tc.fn(tc.err); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}
