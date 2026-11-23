package connectors

import "fmt"

const (
	KindSubscribe = "subscribe"
	KindHistory   = "history"
	KindPresence  = "presence"
	KindPublish   = "publish"
)

// MaxHistoryCount is the most messages a History Connector may return per call; the backend enforces the same limit.
const MaxHistoryCount = 100

// Caller access and consent text, as the dashboard shows it.
const ConsentNotice = "This keyset has no Access Manager, so Blocks cannot limit what a caller reads: " +
	"whoever receives its subscribe key can read messages, history and presence on every channel of the keyset, " +
	"and keeps the key after the Connector is revoked. Every hand-out of the subscribe key is audited."

var callerAccess = map[string]string{
	KindSubscribe: "Callers of this agent can read messages, history and presence on the channels it covers.",
	KindHistory:   "Callers of this agent can read history on the channels it covers.",
	KindPresence:  "Callers of this agent can read presence on the channels it covers.",
	KindPublish:   "Callers of this agent can send its named message into the app.",
}

// CallerAccessNotice is "" for a kind that cannot be created.
func CallerAccessNotice(kind string) string { return callerAccess[kind] }

type KindOption struct {
	Kind      string
	Available bool
	Reason    string
}

const (
	ReasonNeedsPersistence = "History needs Message Persistence on this keyset"
	ReasonNeedsPresence    = "Presence needs Presence on this keyset"
)

// KindOptions lists the kinds a keyset can serve, in display order. The function kind is never offered.
func KindOptions(f KeysetFeatures) []KindOption {
	opts := []KindOption{
		{Kind: KindSubscribe, Available: true},
		{Kind: KindHistory, Available: f.Persistence},
		{Kind: KindPresence, Available: f.Presence},
		{Kind: KindPublish, Available: true},
	}
	opts[1].Reason = unavailableReason(opts[1], ReasonNeedsPersistence)
	opts[2].Reason = unavailableReason(opts[2], ReasonNeedsPresence)
	return opts
}

func unavailableReason(o KindOption, reason string) string {
	if o.Available {
		return ""
	}
	return reason
}

// UnavailableKindError is returned for a kind the keyset does not serve.
type UnavailableKindError struct {
	Kind   string
	Reason string
}

func (e *UnavailableKindError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("kind %q cannot be created", e.Kind)
	}
	return fmt.Sprintf("kind %q is not available: %s", e.Kind, e.Reason)
}

func ValidateKind(f KeysetFeatures, kind string) error {
	for _, o := range KindOptions(f) {
		if o.Kind != kind {
			continue
		}
		if !o.Available {
			return &UnavailableKindError{Kind: kind, Reason: o.Reason}
		}
		return nil
	}
	return &UnavailableKindError{Kind: kind}
}

// RevokeNotice is the access a revoke cannot take back on keyset ks, or "" when it takes back all of it.
func RevokeNotice(ks LinkedKeyset) string {
	switch {
	case ks.Mode == ModeConsent:
		return "This keyset has no Access Manager: callers keep the subscribe key they already received; it cannot be revoked."
	case !ks.Features.RevokeV3:
		return "This keyset has no Revoke v3, so tokens already issued stay valid until they expire, up to 60 minutes."
	}
	return ""
}
