package connectors

import (
	"errors"
	"testing"
)

func TestKindOptionsFollowFeatures(t *testing.T) {
	opts := KindOptions(KeysetFeatures{Presence: true})
	order := []string{KindSubscribe, KindHistory, KindPresence, KindPublish}
	avail := []bool{true, false, true, true}
	if len(opts) != len(order) {
		t.Fatalf("opts = %+v", opts)
	}
	for i, o := range opts {
		if o.Kind != order[i] || o.Available != avail[i] || (o.Available && o.Reason != "") || (!o.Available && o.Reason == "") || CallerAccessNotice(o.Kind) == "" {
			t.Fatalf("option %d = %+v", i, o)
		}
	}
	opts = KindOptions(KeysetFeatures{})
	if opts[1].Reason != "History needs Message Persistence on this keyset" || opts[2].Reason != "Presence needs Presence on this keyset" {
		t.Fatalf("reasons = %q / %q", opts[1].Reason, opts[2].Reason)
	}
	if CallerAccessNotice("function") != "" {
		t.Fatal("the function kind cannot be created, so it has no notice")
	}
}

func TestValidateKind(t *testing.T) {
	f := KeysetFeatures{Persistence: true}
	for _, k := range []string{KindSubscribe, KindHistory, KindPublish} {
		if err := ValidateKind(f, k); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	var ue *UnavailableKindError
	if err := ValidateKind(f, KindPresence); !errors.As(err, &ue) || ue.Reason != ReasonNeedsPresence {
		t.Fatalf("presence: %v", err)
	}
	for _, k := range []string{"function", "bogus", ""} {
		if err := ValidateKind(KeysetFeatures{Persistence: true, Presence: true}, k); !errors.As(err, &ue) {
			t.Fatalf("%q must be refused: %v", k, err)
		}
	}
}
