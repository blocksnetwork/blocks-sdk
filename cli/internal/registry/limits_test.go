package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The overlay used to apply each field only when present, so a partial response left the
// rest at this build's compiled-in defaults — bounds from two sources presented as one
// deployment's rules, with no way to tell which came from where. The schema makes all six
// required, so a body missing any of them is not this endpoint's answer.
func TestPartialOrMalformedPricingLimitsAreRejectedWholesale(t *testing.T) {
	full := `{"minPricePerTask":"0.01","minPricePerMinute":"0.02","maxPricePerTask":"5.00","maxPricePerMinute":"6.00","maxFreeTasksAllowed":7,"maxFreeMinutesAllowed":8}`

	cases := []struct {
		name, body string
		wantUsed   bool
	}{
		{"complete response is used", full, true},
		{"missing one price", `{"minPricePerMinute":"0.02","maxPricePerTask":"5.00","maxPricePerMinute":"6.00","maxFreeTasksAllowed":7,"maxFreeMinutesAllowed":8}`, false},
		{"missing a free-unit ceiling", `{"minPricePerTask":"0.01","minPricePerMinute":"0.02","maxPricePerTask":"5.00","maxPricePerMinute":"6.00","maxFreeTasksAllowed":7}`, false},
		{"a price that is not a decimal", `{"minPricePerTask":"abc","minPricePerMinute":"0.02","maxPricePerTask":"5.00","maxPricePerMinute":"6.00","maxFreeTasksAllowed":7,"maxFreeMinutesAllowed":8}`, false},
		{"empty object", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got := FetchPricingLimits(srv.URL)
			defaults := DefaultPricingLimits()

			if tc.wantUsed {
				if got == defaults {
					t.Fatal("a complete response must be used, not discarded")
				}
				if got.MaxPricePerTask != "5.00" || got.MaxFreeMinutesAllowed != 8 {
					t.Errorf("the deployment's own bounds must be carried through, got %+v", got)
				}
				return
			}
			if got != defaults {
				t.Errorf("a response that does not meet the contract must be discarded whole, got %+v", got)
			}
		})
	}
}
