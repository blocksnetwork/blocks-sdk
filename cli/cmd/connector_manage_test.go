package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
)

const revokeRoute = "POST " + connectorsAPI + "/exposures/x1/revoke"
const patchRoute = "PATCH " + connectorsAPI + "/exposures/x1"

func exposuresFake(t *testing.T, xs ...map[string]any) *connectorFake {
	f := newConnectorFake(t)
	f.reply("GET "+connectorsAPI+"/exposures", 200, exposuresJSON(xs...))
	f.reply(revokeRoute, 200, mustJSON(map[string]any{"exposure": exposureView("x1", "live", "subscribe", true)}))
	f.reply(patchRoute, 200, mustJSON(map[string]any{"exposure": exposureView("x1", "live", "subscribe", false)}))
	return f
}

func TestConnectorEditAndRevokeSendNothingWithoutExactlyOneActiveMatch(t *testing.T) {
	for line, xs := range map[string][]map[string]any{
		"edit live --display-name X": {exposureView("x1", "live", "subscribe", true)},
		"revoke live --yes":          {exposureView("x1", "live", "subscribe", false), exposureView("x2", "live", "history", false)},
	} {
		f := exposuresFake(t, xs...)
		runConnector(t, f, "", args(line)...).wantErr(t)
		f.wantUntouched(t, "PATCH", "revoke")
	}
}

func TestConnectorRevokeAndKeysetDisableRetryWhatIsAlreadyGone(t *testing.T) {
	f := exposuresFake(t, exposureView("x1", "live", "subscribe", true))
	mustRun(t, f, "", "revoke", "live").wantOut(t, "already revoked")
	disableRoute := "POST " + connectorsAPI + "/keysets/lk-1/disable"
	disabled := keysetView("lk-1", "Prod", "sub-1", "access_manager", "admin_api", "disabled", true)
	f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON(disabled))
	f.reply(disableRoute, 200, mustJSON(disabled))
	mustRun(t, f, "", "keyset", "disable", "Prod").wantOut(t, "already disabled")
	if !f.requested(revokeRoute) || !f.requested(disableRoute) {
		t.Fatalf("a repeat revoke or disable did not reach the backend: %v", f.log)
	}
}

func TestConnectorEditSendsOnlyGivenFieldsAndTheWholeMergedConfig(t *testing.T) {
	pub := exposureView("x1", "live", "publish", false)
	pub["config"] = map[string]any{"actionName": "send", "inputSchema": map[string]any{"type": "object"}}
	f := exposuresFake(t, pub)
	mustRun(t, f, "", "edit", "live", "--custom-message-type", "ping")
	body := f.body(patchRoute)
	if cfg, _ := body["config"].(map[string]any); len(body) != 1 || cfg["customMessageType"] != "ping" || cfg["actionName"] != "send" || cfg["inputSchema"] == nil {
		t.Fatalf("PATCH body = %v", body)
	}
}

func TestConnectorEditRefusesWithoutFlagsOrWithAFlagOfAnotherKind(t *testing.T) {
	f := exposuresFake(t, exposureView("x1", "live", "subscribe", false))
	for _, line := range []string{"edit live", "edit live --history-max 5", "edit live --keyset Prod"} {
		runConnector(t, f, "", args(line)...).wantErr(t)
	}
	f.wantUntouched(t, "PATCH")
}

func TestConnectorRevokeAsksFirstAndDefaultsToNo(t *testing.T) {
	f := exposuresFake(t, exposureView("x1", "live", "subscribe", false))
	runConnector(t, f, "", "revoke", "live").wantErr(t, "--yes")
	mustRun(t, f, "\n", "revoke", "live").wantOut(t, "Cancelled: nothing was revoked.")
	f.wantUntouched(t, "revoke")
}

func TestConnectorRevokeWarnsAboutAccessARevokeCannotTakeBack(t *testing.T) {
	noRevokeV3 := keysetView("lk-1", "Prod", "sub-1", "access_manager", "admin_api", "active", true)
	noRevokeV3["features"].(map[string]any)["revokeV3"] = false
	for want, keyset := range map[string]map[string]any{
		"keep the subscribe key": keysetView("lk-1", "Prod", "sub-1", "consent", "admin_api", "active", true),
		"up to 60 minutes":       noRevokeV3,
		"":                       keysetView("lk-1", "Prod", "sub-1", "access_manager", "admin_api", "active", true),
	} {
		f := exposuresFake(t, exposureView("x1", "live", "subscribe", false))
		f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON(keyset))
		res := mustRun(t, f, "", "revoke", "live", "--yes")
		warned := strings.Contains(res.stdout, "cannot be revoked") || strings.Contains(res.stdout, "stay valid")
		if !f.requested(revokeRoute) || (want == "") == warned || !strings.Contains(res.stdout, want) {
			t.Fatalf("revoked %v, want notice %q:\n%s", f.requested(revokeRoute), want, res.stdout)
		}
	}
}

func TestConnectorListHidesRevokedUnlessAll(t *testing.T) {
	f := exposuresFake(t, rawExposure([]string{"a"}, []string{}), exposureView("x2", "old", "history", true))
	if res := runConnector(t, f, "", "list"); !strings.Contains(res.stdout, "live") || strings.Contains(res.stdout, "old") {
		t.Fatalf("list:\n%s", res.stdout)
	}
	runConnector(t, f, "", "list", "--all").wantOut(t, "old", "revoked")
	js := runConnector(t, f, "", "list", "--all", "--json")
	var got []connectors.Exposure
	if err := json.Unmarshal([]byte(js.stdout), &got); err != nil || len(got) != 2 || got[0].ID != "x1" || got[1].RevokedAt == nil {
		t.Fatalf("json (%v):\n%s", err, js.stdout)
	}
	if !strings.Contains(strings.Join(strings.Fields(js.stdout), ""), `"rawSelector":{"channels":["a"],"patterns":[]}`) {
		t.Fatalf("an empty raw selector list was dropped:\n%s", js.stdout)
	}
}

func TestConnectorCommandsStopWhenConnectorsAreOff(t *testing.T) {
	f := newConnectorFake(t)
	f.reply("GET /api/v1/orgs", 200, `{"orgs":[{"id":"org-1","name":"Engineering","pubnubConnectorsEnabled":false}]}`)
	if res := runConnector(t, f, "", "list"); res.errorMsg != "PubNub Connectors are not turned on for this deployment." {
		t.Fatalf("error = %v", res.err)
	}
	f.wantUntouched(t, "pubnub-connectors")
}
