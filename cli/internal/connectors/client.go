package connectors

import (
	"context"
	"net/http"
	"net/url"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

const (
	basePath = "/api/v1/pubnub-connectors"
	orgsPath = "/api/v1/orgs"
)

// Client calls the PubNub Connectors routes. Failures stay *blocksapi.APIError.
type Client struct {
	api *blocksapi.Client
}

func NewClient(api *blocksapi.Client) *Client { return &Client{api: api} }

func seg(s string) string { return url.PathEscape(s) }

func (c *Client) ListAccountConnections(ctx context.Context) ([]AccountConnection, error) {
	var out struct {
		Connections []AccountConnection `json:"connections"`
	}
	err := c.api.DoJSON(ctx, http.MethodGet, basePath+"/account", nil, &out)
	return out.Connections, err
}

func (c *Client) ListPickerApps(ctx context.Context, connectionID string) ([]PickerApp, error) {
	var out struct {
		Apps []PickerApp `json:"apps"`
	}
	err := c.api.DoJSON(ctx, http.MethodGet, basePath+"/account/"+seg(connectionID)+"/keysets", nil, &out)
	return out.Apps, err
}

func (c *Client) DisconnectAccount(ctx context.Context, connectionID string) error {
	return c.api.DoJSON(ctx, http.MethodDelete, basePath+"/account/"+seg(connectionID), nil, nil)
}

func (c *Client) ListKeysets(ctx context.Context) ([]LinkedKeyset, error) {
	var out struct {
		Keysets []LinkedKeyset `json:"keysets"`
	}
	err := c.api.DoJSON(ctx, http.MethodGet, basePath+"/keysets", nil, &out)
	return out.Keysets, err
}

func (c *Client) LinkKeyset(ctx context.Context, req LinkKeysetRequest) (LinkedKeyset, error) {
	var out LinkedKeyset
	err := c.api.DoJSON(ctx, http.MethodPost, basePath+"/keysets", req, &out)
	return out, err
}

func (c *Client) ValidateKeyset(ctx context.Context, id string) (ValidatedKeyset, error) {
	var out ValidatedKeyset
	err := c.api.DoJSON(ctx, http.MethodPost, basePath+"/keysets/"+seg(id)+"/validate", nil, &out)
	return out, err
}

func (c *Client) DisableKeyset(ctx context.Context, id string) (LinkedKeyset, error) {
	var out LinkedKeyset
	err := c.api.DoJSON(ctx, http.MethodPost, basePath+"/keysets/"+seg(id)+"/disable", nil, &out)
	return out, err
}

func (c *Client) ListExposures(ctx context.Context) ([]Exposure, error) {
	var out struct {
		Exposures []Exposure `json:"exposures"`
	}
	err := c.api.DoJSON(ctx, http.MethodGet, basePath+"/exposures", nil, &out)
	return out.Exposures, err
}

func (c *Client) CreateExposure(ctx context.Context, req CreateExposureRequest) (Exposure, error) {
	return c.exposureCall(ctx, http.MethodPost, basePath+"/exposures", req)
}

func (c *Client) UpdateExposure(ctx context.Context, id string, req UpdateExposureRequest) (Exposure, error) {
	return c.exposureCall(ctx, http.MethodPatch, basePath+"/exposures/"+seg(id), req)
}

func (c *Client) RevokeExposure(ctx context.Context, id string) (Exposure, error) {
	return c.exposureCall(ctx, http.MethodPost, basePath+"/exposures/"+seg(id)+"/revoke", nil)
}

func (c *Client) exposureCall(ctx context.Context, method, path string, body any) (Exposure, error) {
	var out struct {
		Exposure Exposure `json:"exposure"`
	}
	err := c.api.DoJSON(ctx, method, path, body, &out)
	return out.Exposure, err
}

func (c *Client) ListOrgs(ctx context.Context) ([]Org, error) {
	var out struct {
		Orgs []Org `json:"orgs"`
	}
	err := c.api.DoJSON(ctx, http.MethodGet, orgsPath, nil, &out)
	return out.Orgs, err
}
