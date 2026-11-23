package connectors

import (
	"errors"
	"net/http"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

// Backend error codes the commands tell apart.
const (
	CodeAgentNameTaken          = "AgentNameTaken"
	CodeAgentOrgMismatch        = "AgentOrgMismatch"
	CodeNotFound                = "NOT_FOUND"
	CodeExposureOrgMismatch     = "PUBNUB_EXPOSURE_ORG_MISMATCH"
	CodeExposureOrgNotAllowed   = "PUBNUB_EXPOSURE_ORG_NOT_ALLOWED"
	CodeExposureConsentRequired = "PUBNUB_EXPOSURE_CONSENT_REQUIRED"
	CodeExposureKindUnsupported = "PUBNUB_EXPOSURE_KIND_UNSUPPORTED"
	CodeExposureRevoked         = "PUBNUB_EXPOSURE_REVOKED"
	CodeExposureNotFound        = "PUBNUB_EXPOSURE_NOT_FOUND"
	CodeKeysetDisabled          = "LINKED_KEYSET_DISABLED"
	CodeKeysetNotFound          = "LINKED_KEYSET_NOT_FOUND"
	CodeKeysetAlreadyLinked     = "LINKED_KEYSET_ALREADY_LINKED"
	CodeConnectionNotFound      = "PUBNUB_ACCOUNT_CONNECTION_NOT_FOUND"
	CodeConnectionDead          = "PUBNUB_ACCOUNT_CONNECTION_DEAD"
	CodeAccountUnavailable      = "PUBNUB_ACCOUNT_UNAVAILABLE"
	CodeInsufficientRights      = "PUBNUB_ACCOUNT_INSUFFICIENT_RIGHTS"
	CodeAdminAPIRateLimited     = "PUBNUB_ADMIN_API_RATE_LIMITED"
)

func apiError(err error) *blocksapi.APIError {
	var apiErr *blocksapi.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return nil
}

// IsNameTaken covers every way the backend says an agent name is not available to this org.
func IsNameTaken(err error) bool {
	e := apiError(err)
	if e == nil {
		return false
	}
	switch {
	case e.Code == CodeAgentNameTaken:
		return e.StatusCode == http.StatusConflict || e.StatusCode == http.StatusForbidden
	case e.Code == CodeAgentOrgMismatch:
		return e.StatusCode == http.StatusForbidden
	}
	return false
}

// IsPermissionDenied is a 403 without a code: the caller lacks the permission in this org.
func IsPermissionDenied(err error) bool {
	e := apiError(err)
	return e != nil && e.StatusCode == http.StatusForbidden && e.Code == ""
}

// IsFeatureOff is the 404 the backend answers when Connectors, or the account routes, are off.
func IsFeatureOff(err error) bool {
	e := apiError(err)
	return e != nil && e.StatusCode == http.StatusNotFound && e.Code == CodeNotFound
}

func IsAlreadyLinked(err error) bool {
	e := apiError(err)
	return e != nil && e.StatusCode == http.StatusConflict && e.Code == CodeKeysetAlreadyLinked
}
