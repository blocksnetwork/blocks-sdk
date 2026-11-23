package connectors

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

type recorded struct {
	method, path, body string
}

func newTestClient(t *testing.T, status int, response string) (*Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*rec = recorded{r.Method, r.URL.EscapedPath(), string(b)}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(ts.Close)
	return NewClient(blocksapi.NewClient(ts.URL, "bk_test")), rec
}

func TestPickerEscapesSegmentsAndDecodesNullableFields(t *testing.T) {
	c, rec := newTestClient(t, 200, `{"apps":[{"id":"a1","name":null,"keysets":[
{"id":"k1","name":"Prod","subscribeKey":"sub-c-1","linkedKeysetId":null},
{"id":"k2","name":null,"subscribeKey":"sub-c-2","linkedKeysetId":"lk-9"}]}]}`)
	apps, err := c.ListPickerApps(context.Background(), "a/b c")
	if err != nil || rec.path != "/api/v1/pubnub-connectors/account/a%2Fb%20c/keysets" {
		t.Fatalf("path = %s, err %v", rec.path, err)
	}
	ks := apps[0].Keysets
	if apps[0].Name != nil || ks[0].LinkedKeysetID != nil || ks[1].Name != nil || *ks[1].LinkedKeysetID != "lk-9" {
		t.Fatalf("nullable fields lost: %+v", apps)
	}
}

func TestKeysetWritesReturnBareViews(t *testing.T) {
	view := `{"id":"lk1","label":"Acme / Prod","subscribeKey":"sub-c-1","origin":null,"mode":"access_manager",
"features":{"accessManager":true,"persistence":true,"presence":true,"wildcard":true,"revokeV3":true,"appContext":false},
"source":"admin_api","status":"active","createdBy":"u1","createdAt":"t0","updatedAt":"t1","disabledAt":null,
"pubnubKeysetId":"4421","pubnubAccountId":"42","accountConnectionId":"c1","configChecked":false,"configSkippedReason":"no rights"}`
	c, rec := newTestClient(t, 201, view)
	got, err := c.LinkKeyset(context.Background(), LinkKeysetRequest{
		Source: LinkSourceAdminAPI, Label: "Acme / Prod", ConnectionID: "c1", PubnubKeysetID: "4421"})
	if err != nil || rec.body != `{"source":"admin_api","label":"Acme / Prod","connectionId":"c1","pubnubKeysetId":"4421"}` {
		t.Fatalf("body = %s, err %v", rec.body, err)
	}
	if got.ID != "lk1" || got.Mode != "access_manager" || *got.PubnubKeysetID != "4421" || got.Features.Wildcard == nil || !*got.Features.Wildcard {
		t.Fatalf("got %+v", got)
	}

	v, err := c.ValidateKeyset(context.Background(), "lk1")
	if err != nil || rec.method != "POST" || rec.path != "/api/v1/pubnub-connectors/keysets/lk1/validate" || v.ID != "lk1" || v.ConfigSkippedReason != "no rights" {
		t.Fatalf("validate: %+v %+v", rec, v)
	}
	if _, err := c.DisableKeyset(context.Background(), "lk1"); err != nil || rec.path != "/api/v1/pubnub-connectors/keysets/lk1/disable" {
		t.Fatalf("disable: %v %+v", err, rec)
	}
}

func TestExposureRoutesUnwrapAndShapeBodies(t *testing.T) {
	exposure := `{"exposure":{"id":"e1","orgId":"org-1","agentId":"ag1","agentName":"live_scores","displayName":"Live",
"description":null,"linkedKeysetId":"lk1","kind":"history","channelTemplate":"live.{show}","templateParam":"show",
"rawAccess":false,"rawSelector":null,"config":{"historyMaxCount":50},"consentedBy":null,"consentedAt":null,
"createdBy":"u1","createdAt":"t0","revokedBy":null,"revokedAt":null,"allValues":{"eligible":true,"pattern":"live.*"}}}`
	c, rec := newTestClient(t, 200, exposure)
	got, err := c.CreateExposure(context.Background(), CreateExposureRequest{
		OrgID: "org-1", LinkedKeysetID: "lk1", Kind: KindHistory, AgentName: "live_scores",
		ChannelTemplate: "live.{show}", Config: map[string]any{"historyMaxCount": 50}})
	if err != nil || rec.method != "POST" || rec.path != "/api/v1/pubnub-connectors/exposures" {
		t.Fatalf("request %+v, err %v", rec, err)
	}
	if rec.body != `{"orgId":"org-1","linkedKeysetId":"lk1","kind":"history","agentName":"live_scores","channelTemplate":"live.{show}","config":{"historyMaxCount":50}}` {
		t.Fatalf("optional fields leaked: %s", rec.body)
	}
	if got.AgentName != "live_scores" || got.Revoked() || !got.AllValues.Eligible || got.AllValues.Pattern != "live.*" ||
		got.Config["historyMaxCount"] != float64(50) || *got.TemplateParam != "show" {
		t.Fatalf("got %+v", got)
	}

	name, empty := "New name", map[string]any{}
	_, err = c.UpdateExposure(context.Background(), "e/1", UpdateExposureRequest{DisplayName: &name, Config: &empty})
	if err != nil || rec.method != "PATCH" || rec.path != "/api/v1/pubnub-connectors/exposures/e%2F1" || rec.body != `{"displayName":"New name","config":{}}` {
		t.Fatalf("update %+v, err %v", rec, err)
	}
	if _, err := c.RevokeExposure(context.Background(), "e1"); err != nil || rec.path != "/api/v1/pubnub-connectors/exposures/e1/revoke" || rec.body != "" {
		t.Fatalf("revoke %+v, err %v", rec, err)
	}
}

func TestListEnvelopesAndOrgs(t *testing.T) {
	c, rec := newTestClient(t, 200, `{"exposures":[{"id":"e1","agentName":"a","revokedAt":"t9","allValues":{"eligible":false,"reason":"no param"}}]}`)
	ex, err := c.ListExposures(context.Background())
	if err != nil || len(ex) != 1 || !ex[0].Revoked() || ex[0].AllValues.Reason != "no param" {
		t.Fatalf("exposures %+v %v", ex, err)
	}
	c, _ = newTestClient(t, 200, `{"keysets":[{"id":"lk1","status":"disabled","disabledAt":"t3"}]}`)
	ks, err := c.ListKeysets(context.Background())
	if err != nil || len(ks) != 1 || ks[0].Status != KeysetStatusDisabled || *ks[0].DisabledAt != "t3" {
		t.Fatalf("keysets %+v %v", ks, err)
	}
	c, rec = newTestClient(t, 200, `{"orgs":[{"id":"org-1","name":"Acme","pubnubConnectorsEnabled":true}]}`)
	orgs, err := c.ListOrgs(context.Background())
	if err != nil || rec.path != "/api/v1/orgs" || orgs[0] != (Org{ID: "org-1", Name: "Acme", PubnubConnectorsEnabled: true}) {
		t.Fatalf("orgs %+v %v", orgs, err)
	}
}

func TestDisconnectAndErrorsStayAPIErrors(t *testing.T) {
	c, rec := newTestClient(t, 200, `{"ok":true}`)
	if err := c.DisconnectAccount(context.Background(), "c1"); err != nil || rec.method != "DELETE" || rec.path != "/api/v1/pubnub-connectors/account/c1" {
		t.Fatalf("disconnect: %v %+v", err, rec)
	}
	c, _ = newTestClient(t, 404, `{"error":"Not found","code":"NOT_FOUND"}`)
	_, err := c.ListAccountConnections(context.Background())
	var apiErr *blocksapi.APIError
	if !errors.As(err, &apiErr) || !IsFeatureOff(err) {
		t.Fatalf("err = %v", err)
	}
}
