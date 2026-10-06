package registry

import "testing"

type selectCall struct {
	prompt     string
	options    []string
	defaultIdx int
}

func stubTerminalSelect(t *testing.T, pick int) *selectCall {
	t.Helper()
	origTerm, origSelect := stdinIsTerminal, selectOption
	t.Cleanup(func() { stdinIsTerminal, selectOption = origTerm, origSelect })
	call := &selectCall{}
	stdinIsTerminal = func() bool { return true }
	selectOption = func(prompt string, options []string, defaultIdx int, _ string) (int, error) {
		*call = selectCall{prompt, options, defaultIdx}
		return pick, nil
	}
	return call
}

func TestListingUsesArrowPickerInTerminal(t *testing.T) {
	for pick, want := range map[int]string{listingPublicIdx: "public", listingPrivateIdx: "private"} {
		call := stubTerminalSelect(t, pick)
		got, err := promptListingSelection(nil)
		if err != nil || got != want {
			t.Errorf("pick %d: got %q, %v; want %q", pick, got, err, want)
		}
		if call.prompt != listingQuestion || len(call.options) != 2 || call.defaultIdx != listingPrivateIdx {
			t.Errorf("picker asked %+v, want the listing question defaulting to Private", *call)
		}
	}
}

func TestBillingUsesArrowPickerInTerminal(t *testing.T) {
	for pick, want := range map[int]string{billingFreeIdx: "free", billingPaidIdx: "paid"} {
		call := stubTerminalSelect(t, pick)
		got, err := promptBillingMode(nil)
		if err != nil || got != want {
			t.Errorf("pick %d: got %q, %v; want %q", pick, got, err, want)
		}
		if call.prompt != billingQuestion || call.defaultIdx != billingFreeIdx {
			t.Errorf("picker asked %+v, want the billing question defaulting to Free", *call)
		}
	}
}
