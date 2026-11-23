package connectors

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrAmbiguous        = errors.New("ambiguous")
	ErrConnectionDead   = errors.New("connection is dead")
	ErrNotOwnConnection = errors.New("connection belongs to another user")
	ErrKeysetDisabled   = errors.New("keyset is disabled")
	ErrExposureRevoked  = errors.New("connector is revoked")
)

func ambiguous(kind, ref string, ids []string) error {
	return fmt.Errorf("%w: %s %q matches %d (ids: %s)", ErrAmbiguous, kind, ref, len(ids), strings.Join(ids, ", "))
}

// ConnectionChoice is what to do about the org's PubNub account connection. Exactly one of Use,
// Candidates and NeedsBrowser is set. OwnDead is set whenever the caller has a dead connection and
// nothing was chosen.
type ConnectionChoice struct {
	Use          *AccountConnection
	Candidates   []AccountConnection
	NeedsBrowser bool
	OwnDead      *AccountConnection
}

// ChooseConnection prefers the caller's own active connection, then any active one in the org.
// preferredRef (an id or an exact name) overrides that and must name an active connection.
func ChooseConnection(conns []AccountConnection, preferredRef string) (ConnectionChoice, error) {
	if preferredRef != "" {
		conn, err := ResolveConnection(conns, preferredRef)
		if err != nil {
			return ConnectionChoice{}, err
		}
		if conn.Status != ConnectionActive {
			return ConnectionChoice{}, fmt.Errorf("%w: %s", ErrConnectionDead, conn.ID)
		}
		return ConnectionChoice{Use: &conn}, nil
	}
	var own, others []AccountConnection
	var ownDead *AccountConnection
	for i, c := range conns {
		switch {
		case c.Status == ConnectionActive && c.IsMine:
			own = append(own, c)
		case c.Status == ConnectionActive:
			others = append(others, c)
		case c.IsMine && ownDead == nil:
			ownDead = &conns[i]
		}
	}
	for _, group := range [][]AccountConnection{own, others} {
		switch len(group) {
		case 0:
		case 1:
			return ConnectionChoice{Use: &group[0]}, nil
		default:
			return ConnectionChoice{Candidates: group}, nil
		}
	}
	return ConnectionChoice{NeedsBrowser: true, OwnDead: ownDead}, nil
}

// ResolveConnection matches an id first, then an exact name.
func ResolveConnection(conns []AccountConnection, ref string) (AccountConnection, error) {
	for _, c := range conns {
		if c.ID == ref {
			return c, nil
		}
	}
	var matches []AccountConnection
	var ids []string
	for _, c := range conns {
		if c.Name == ref {
			matches = append(matches, c)
			ids = append(ids, c.ID)
		}
	}
	switch len(matches) {
	case 0:
		return AccountConnection{}, fmt.Errorf("%w: PubNub account connection %q", ErrNotFound, ref)
	case 1:
		return matches[0], nil
	}
	return AccountConnection{}, ambiguous("PubNub account connection", ref, ids)
}

// CheckDisconnectable refuses another user's connection: only its owner can disconnect it, and the
// backend's answer for a stranger is the same 404 as for a missing id.
func CheckDisconnectable(c AccountConnection) error {
	if !c.IsMine {
		return fmt.Errorf("%w: %s", ErrNotOwnConnection, c.ID)
	}
	return nil
}

// VisibleConnections hides other users' dead rows unless all is set.
func VisibleConnections(conns []AccountConnection, all bool) []AccountConnection {
	return filter(conns, all, func(c AccountConnection) bool {
		return c.Status == ConnectionActive || c.IsMine
	})
}

func VisibleKeysets(keysets []LinkedKeyset, all bool) []LinkedKeyset {
	return filter(keysets, all, func(k LinkedKeyset) bool { return k.Status != KeysetStatusDisabled })
}

func VisibleExposures(exposures []Exposure, all bool) []Exposure {
	return filter(exposures, all, func(e Exposure) bool { return !e.Revoked() })
}

func filter[T any](in []T, all bool, keep func(T) bool) []T {
	if all {
		return in
	}
	out := make([]T, 0, len(in))
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

// ResolveKeyset matches an id first, then an exact label among active keysets. A match that is
// disabled is reported as such rather than as missing.
func ResolveKeyset(keysets []LinkedKeyset, ref string) (LinkedKeyset, error) {
	for _, k := range keysets {
		if k.ID == ref {
			return activeKeyset(k)
		}
	}
	var active []LinkedKeyset
	var ids []string
	var disabled *LinkedKeyset
	for i, k := range keysets {
		if k.Label != ref {
			continue
		}
		if k.Status == KeysetStatusDisabled {
			disabled = &keysets[i]
			continue
		}
		active = append(active, k)
		ids = append(ids, k.ID)
	}
	switch {
	case len(active) == 1:
		return active[0], nil
	case len(active) > 1:
		return LinkedKeyset{}, ambiguous("keyset", ref, ids)
	case disabled != nil:
		return activeKeyset(*disabled)
	}
	return LinkedKeyset{}, fmt.Errorf("%w: keyset %q", ErrNotFound, ref)
}

func activeKeyset(k LinkedKeyset) (LinkedKeyset, error) {
	if k.Status == KeysetStatusDisabled {
		return k, fmt.Errorf("%w: %s", ErrKeysetDisabled, k.ID)
	}
	return k, nil
}

// FindLinkedBySubscribeKey finds the active keyset already linked for a subscribe key.
func FindLinkedBySubscribeKey(keysets []LinkedKeyset, subscribeKey string) (LinkedKeyset, bool) {
	for _, k := range keysets {
		if k.SubscribeKey == subscribeKey && k.Status != KeysetStatusDisabled {
			return k, true
		}
	}
	return LinkedKeyset{}, false
}

func FindLinkedByPubnubKeysetID(keysets []LinkedKeyset, pubnubKeysetID string) (LinkedKeyset, bool) {
	for _, k := range keysets {
		if k.PubnubKeysetID != nil && *k.PubnubKeysetID == pubnubKeysetID && k.Status != KeysetStatusDisabled {
			return k, true
		}
	}
	return LinkedKeyset{}, false
}

// MaxLabelLength is the backend's keyset label limit, in UTF-16 code units (JavaScript string length).
const MaxLabelLength = 100

func DefaultLinkLabel(appName, keysetName *string, pubnubKeysetID string) string {
	label := pubnubKeysetID
	if appName != nil && keysetName != nil && *appName != "" && *keysetName != "" {
		label = *appName + " / " + *keysetName
	}
	units := 0
	for i, r := range label {
		units += utf16.RuneLen(r)
		if units > MaxLabelLength {
			return label[:i]
		}
	}
	return label
}

// ResolveExposureByAgentName finds the one active Exposure for an agent name. It never picks
// between two.
func ResolveExposureByAgentName(exposures []Exposure, name string) (Exposure, error) {
	var active []Exposure
	var ids []string
	revoked := false
	for _, e := range exposures {
		if e.AgentName != name {
			continue
		}
		if e.Revoked() {
			revoked = true
			continue
		}
		active = append(active, e)
		ids = append(ids, e.ID)
	}
	switch {
	case len(active) == 1:
		return active[0], nil
	case len(active) > 1:
		return Exposure{}, ambiguous("connector", name, ids)
	case revoked:
		return Exposure{}, fmt.Errorf("%w: %s", ErrExposureRevoked, name)
	}
	return Exposure{}, fmt.Errorf("%w: connector %q", ErrNotFound, name)
}
