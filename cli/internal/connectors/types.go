// Package connectors is the transport and decision logic behind the 'blocks connector' commands.
package connectors

const (
	ConnectionActive = "active"
	ConnectionDead   = "dead"

	KeysetStatusDisabled = "disabled"

	ModeConsent = "consent"
)

type ConnectedBy struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

// AccountConnection is a PubNub account connected to an org. UpdatedAt is the server's value and is
// only ever compared for equality.
type AccountConnection struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	PubnubAccountID string      `json:"pubnubAccountId"`
	Status          string      `json:"status"`
	ConnectedBy     ConnectedBy `json:"connectedBy"`
	IsMine          bool        `json:"isMine"`
	CreatedAt       string      `json:"createdAt"`
	UpdatedAt       string      `json:"updatedAt"`
}

type PickerKeyset struct {
	ID             string  `json:"id"`
	Name           *string `json:"name"`
	SubscribeKey   string  `json:"subscribeKey"`
	LinkedKeysetID *string `json:"linkedKeysetId"`
}

type PickerApp struct {
	ID      string         `json:"id"`
	Name    *string        `json:"name"`
	Keysets []PickerKeyset `json:"keysets"`
}

// KeysetFeatures is what a keyset serves. Wildcard is nil when the backend could not tell.
type KeysetFeatures struct {
	AccessManager bool  `json:"accessManager"`
	Persistence   bool  `json:"persistence"`
	Presence      bool  `json:"presence"`
	Wildcard      *bool `json:"wildcard"`
	RevokeV3      bool  `json:"revokeV3"`
	AppContext    bool  `json:"appContext"`
}

// LinkedKeyset is a keyset linked to the deployment. It never carries a secret or publish key.
type LinkedKeyset struct {
	ID                  string         `json:"id"`
	Label               string         `json:"label"`
	SubscribeKey        string         `json:"subscribeKey"`
	Origin              *string        `json:"origin"`
	Mode                string         `json:"mode"`
	Features            KeysetFeatures `json:"features"`
	Source              string         `json:"source"`
	Status              string         `json:"status"`
	CreatedBy           *string        `json:"createdBy"`
	CreatedAt           string         `json:"createdAt"`
	UpdatedAt           string         `json:"updatedAt"`
	DisabledAt          *string        `json:"disabledAt"`
	PubnubKeysetID      *string        `json:"pubnubKeysetId"`
	PubnubAccountID     *string        `json:"pubnubAccountId"`
	AccountConnectionID *string        `json:"accountConnectionId"`
}

type ValidatedKeyset struct {
	LinkedKeyset
	ConfigChecked       bool   `json:"configChecked"`
	ConfigSkippedReason string `json:"configSkippedReason,omitempty"`
}

type RawSelector struct {
	Channels []string `json:"channels,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
}

// RawSelectorView keeps empty lists, which the request type omits, so --json matches the response.
type RawSelectorView struct {
	Channels []string `json:"channels"`
	Patterns []string `json:"patterns"`
}

// AllValues says whether a template may be called with a wildcard in place of its parameter.
type AllValues struct {
	Eligible bool   `json:"eligible"`
	Pattern  string `json:"pattern,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type Exposure struct {
	ID              string           `json:"id"`
	OrgID           *string          `json:"orgId"`
	AgentID         string           `json:"agentId"`
	AgentName       string           `json:"agentName"`
	DisplayName     string           `json:"displayName"`
	Description     *string          `json:"description"`
	LinkedKeysetID  string           `json:"linkedKeysetId"`
	Kind            string           `json:"kind"`
	ChannelTemplate *string          `json:"channelTemplate"`
	TemplateParam   *string          `json:"templateParam"`
	RawAccess       bool             `json:"rawAccess"`
	RawSelector     *RawSelectorView `json:"rawSelector"`
	Config          map[string]any   `json:"config"`
	ConsentedBy     *string          `json:"consentedBy"`
	ConsentedAt     *string          `json:"consentedAt"`
	CreatedBy       string           `json:"createdBy"`
	CreatedAt       string           `json:"createdAt"`
	RevokedBy       *string          `json:"revokedBy"`
	RevokedAt       *string          `json:"revokedAt"`
	AllValues       AllValues        `json:"allValues"`
}

// Revoked reports whether the Exposure has been revoked.
func (e Exposure) Revoked() bool { return e.RevokedAt != nil }

type Org struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	PubnubConnectorsEnabled bool   `json:"pubnubConnectorsEnabled"`
}

const LinkSourceAdminAPI = "admin_api"

type LinkKeysetRequest struct {
	Source         string `json:"source"`
	Label          string `json:"label"`
	ConnectionID   string `json:"connectionId"`
	PubnubKeysetID string `json:"pubnubKeysetId"`
}

// CreateExposureRequest sets exactly one of ChannelTemplate and RawSelector.
type CreateExposureRequest struct {
	OrgID           string         `json:"orgId"`
	LinkedKeysetID  string         `json:"linkedKeysetId"`
	Kind            string         `json:"kind"`
	AgentName       string         `json:"agentName"`
	DisplayName     string         `json:"displayName,omitempty"`
	Description     string         `json:"description,omitempty"`
	ChannelTemplate string         `json:"channelTemplate,omitempty"`
	RawSelector     *RawSelector   `json:"rawSelector,omitempty"`
	Config          map[string]any `json:"config,omitempty"`
	Consent         bool           `json:"consent,omitempty"`
}

// UpdateExposureRequest sends only the fields that are set. The backend replaces Config whole, so a
// set Config (even an empty map) is the complete new config.
type UpdateExposureRequest struct {
	DisplayName     *string         `json:"displayName,omitempty"`
	Description     *string         `json:"description,omitempty"`
	ChannelTemplate *string         `json:"channelTemplate,omitempty"`
	RawSelector     *RawSelector    `json:"rawSelector,omitempty"`
	Config          *map[string]any `json:"config,omitempty"`
}
