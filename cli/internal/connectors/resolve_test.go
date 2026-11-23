package connectors

import (
	"errors"
	"strings"
	"testing"
)

func conn(id, status string, mine bool) AccountConnection {
	return AccountConnection{ID: id, Name: "name-" + id, Status: status, IsMine: mine, UpdatedAt: "t0"}
}

func TestChooseConnection(t *testing.T) {
	cases := []struct {
		name       string
		conns      []AccountConnection
		wantUse    string
		wantCands  int
		wantBrowse bool
		wantOwnDed string
	}{
		{"own active beats colleague's", []AccountConnection{conn("o", "active", false), conn("m", "active", true)}, "m", 0, false, ""},
		{"several own", []AccountConnection{conn("a", "active", true), conn("b", "active", true), conn("c", "active", false)}, "", 2, false, ""},
		{"only other users' dead rows", []AccountConnection{conn("d", "dead", false)}, "", 0, true, ""},
		{"own dead is signalled", []AccountConnection{conn("d", "dead", true)}, "", 0, true, "d"},
		{"own dead but colleague active", []AccountConnection{conn("d", "dead", true), conn("o", "active", false)}, "o", 0, false, ""},
	}
	id := func(c *AccountConnection) string {
		if c == nil {
			return ""
		}
		return c.ID
	}
	for _, tc := range cases {
		got, err := ChooseConnection(tc.conns, "")
		if err != nil || id(got.Use) != tc.wantUse || len(got.Candidates) != tc.wantCands || got.NeedsBrowser != tc.wantBrowse || id(got.OwnDead) != tc.wantOwnDed {
			t.Errorf("%s: got %+v, err %v", tc.name, got, err)
		}
	}
}

// lookup is one call to a resolver: the id it must find, or the error it must wrap and the ids that error lists.
type lookup struct {
	ref, id string
	err     error
	listed  string
}

func checkLookups(t *testing.T, resolve func(ref string) (string, error), cases ...lookup) {
	t.Helper()
	for _, c := range cases {
		id, err := resolve(c.ref)
		if c.err == nil && (err != nil || id != c.id) || c.err != nil && (!errors.Is(err, c.err) || !strings.Contains(err.Error(), c.listed)) {
			t.Fatalf("%q: got %q, %v", c.ref, id, err)
		}
	}
}

func TestChooseConnectionPreferredRef(t *testing.T) {
	conns := []AccountConnection{conn("a", "active", false), conn("b", "active", true), conn("d", "dead", true),
		{ID: "x", Name: "twin", Status: "active"}, {ID: "y", Name: "twin", Status: "active"}}
	checkLookups(t, func(ref string) (string, error) {
		got, err := ChooseConnection(conns, ref)
		if got.Use == nil {
			return "", err
		}
		return got.Use.ID, err
	}, lookup{ref: "a", id: "a"}, lookup{ref: "name-b", id: "b"}, lookup{ref: "d", err: ErrConnectionDead},
		lookup{ref: "nope", err: ErrNotFound}, lookup{ref: "twin", err: ErrAmbiguous, listed: "x, y"})
}

func TestResolveKeyset(t *testing.T) {
	ks := []LinkedKeyset{
		{ID: "k1", Label: "Prod", Status: "active"},
		{ID: "k2", Label: "Twin", Status: "active"},
		{ID: "k3", Label: "Twin", Status: "active"},
		{ID: "k4", Label: "Old", Status: "disabled"},
		{ID: "k5", Label: "k1", Status: "active"},
	}
	checkLookups(t, func(ref string) (string, error) { k, err := ResolveKeyset(ks, ref); return k.ID, err },
		lookup{ref: "k1", id: "k1"}, // an id wins over a label equal to it
		lookup{ref: "Prod", id: "k1"}, lookup{ref: "Twin", err: ErrAmbiguous, listed: "k2, k3"},
		lookup{ref: "Old", err: ErrKeysetDisabled}, lookup{ref: "k4", err: ErrKeysetDisabled}, lookup{ref: "zzz", err: ErrNotFound})
}

func TestDefaultLinkLabel(t *testing.T) {
	ks := "Production"
	a48, b49, b50 := strings.Repeat("a", 48), strings.Repeat("b", 49), strings.Repeat("é", 50)
	e49, x := strings.Repeat("😀", 49), "x"
	cases := []struct {
		name     string
		app, key *string
		want     string
	}{
		{"null app name", nil, &ks, "4421"},
		{"exactly the maximum", &a48, &b49, a48 + " / " + b49},
		{"one over, cut on a character", &b50, &b50, b50 + " / " + strings.Repeat("é", 47)},
		{"a surrogate pair counts twice", &e49, &x, e49 + " /"},
	}
	for _, tc := range cases {
		if got := DefaultLinkLabel(tc.app, tc.key, "4421"); got != tc.want {
			t.Errorf("%s: got %q", tc.name, got)
		}
	}
}

func TestResolveExposureByAgentName(t *testing.T) {
	rev := "t1"
	ex := []Exposure{{ID: "e1", AgentName: "dup"}, {ID: "e2", AgentName: "dup"}, {ID: "e3", AgentName: "gone", RevokedAt: &rev},
		{ID: "e4", AgentName: "solo", RevokedAt: &rev}, {ID: "e5", AgentName: "solo"}, {ID: "e6", AgentName: "other"}}
	checkLookups(t, func(ref string) (string, error) { e, err := ResolveExposureByAgentName(ex, ref); return e.ID, err },
		lookup{ref: "dup", err: ErrAmbiguous, listed: "e1, e2"}, // two active matches must not pick the first
		lookup{ref: "gone", err: ErrExposureRevoked}, lookup{ref: "missing", err: ErrNotFound}, lookup{ref: "solo", id: "e5"})
}

func TestFindLinkedIgnoresDisabledKeysets(t *testing.T) {
	id := "4421"
	keysets := []LinkedKeyset{
		{ID: "off", SubscribeKey: "sub-1", PubnubKeysetID: &id, Status: KeysetStatusDisabled},
		{ID: "pasted", SubscribeKey: "sub-2"},
		{ID: "on", SubscribeKey: "sub-1", PubnubKeysetID: &id, Status: "active"},
	}
	byID, okID := FindLinkedByPubnubKeysetID(keysets, id)
	bySub, okSub := FindLinkedBySubscribeKey(keysets, "sub-1")
	_, staleID := FindLinkedByPubnubKeysetID(keysets[:2], id)
	_, staleSub := FindLinkedBySubscribeKey(keysets[:2], "sub-1")
	if !okID || byID.ID != "on" || !okSub || bySub.ID != "on" || staleID || staleSub {
		t.Fatalf("by id %+v %v, by subscribe key %+v %v; a disabled keyset was reused: %v %v", byID, okID, bySub, okSub, staleID, staleSub)
	}
}
