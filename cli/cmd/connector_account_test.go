package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
)

func TestConnectorAccountConnectAlwaysUsesTheBrowserInATerminal(t *testing.T) {
	f := newConnectorFake(t) // an active connection by someone else already exists
	f.onBrowser = func() {
		f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON(
			connection("conn-1", "Acme PubNub", "active", false), connection("conn-2", "Mine", "active", true)))
	}
	if res := mustRun(t, f, "\n", "account", "connect"); len(res.browsed) != 1 || !strings.Contains(res.stdout, `connected: "Mine"`) {
		t.Fatalf("browsed %v\n%s", res.browsed, res.stdout)
	}
	if quiet := runConnector(t, f, "", "account", "connect"); quiet.err == nil || len(quiet.browsed) != 0 {
		t.Fatalf("without a terminal: err %v browsed %v", quiet.err, quiet.browsed)
	}
}

func TestConnectorAccountListHidesOthersDeadRowsUnlessAll(t *testing.T) {
	f := newConnectorFake(t)
	f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON(connection("c-live", "Shared", "active", false),
		connection("c-dead-theirs", "TheirsDead", "dead", false), connection("c-dead-mine", "MineDead", "dead", true)))
	if res := runConnector(t, f, "", "account", "list"); !strings.Contains(res.stdout, "Shared") || !strings.Contains(res.stdout, "MineDead") || strings.Contains(res.stdout, "TheirsDead") {
		t.Fatalf("list:\n%s", res.stdout)
	}
	js := runConnector(t, f, "", "account", "list", "--all", "--json")
	var got []connectors.AccountConnection
	if err := json.Unmarshal([]byte(js.stdout), &got); err != nil || len(got) != 3 || got[1].ID != "c-dead-theirs" || got[1].Status != "dead" {
		t.Fatalf("json (%v):\n%s", err, js.stdout)
	}
}

func TestConnectorAccountDisconnectIsOnlyForTheOwner(t *testing.T) {
	f := newConnectorFake(t)
	f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON(
		connection("c-theirs", "Theirs", "active", false), connection("c-mine", "Mine", "active", true)))
	f.reply("DELETE "+connectorsAPI+"/account/c-mine", 200, `{"ok":true}`)
	runConnector(t, f, "", "account", "disconnect", "Theirs").wantErr(t, "only the person who connected")
	runConnector(t, f, "", "account", "disconnect").wantErr(t, "--yes")
	mustRun(t, f, "\n", "account", "disconnect")
	f.wantUntouched(t, "DELETE")
	mustRun(t, f, "", "account", "disconnect", "--yes")
	if !f.requested("DELETE " + connectorsAPI + "/account/c-mine") {
		t.Fatalf("own connection not disconnected: %v", f.log)
	}
}

func TestConnectorKeysetAvailableUsesAnyActiveConnectionAndNamesADeadOwnOne(t *testing.T) {
	f := newConnectorFake(t)
	mustRun(t, f, "", "keyset", "available").wantOut(t, "Using PubNub account", "connected by", "Live", "Prod", "sub-1", "k-2")
	f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON(connection("conn-1", "Mine", "dead", true)))
	runConnector(t, f, "", "keyset", "available").wantErr(t, "no longer works", "blocks connector account connect")
}

func TestConnectorKeysetListHidesDisabledAndShowsNoSecrets(t *testing.T) {
	f := newConnectorFake(t)
	live := keysetView("lk-1", "Prod\x1b[31m", "sub-1", "access_manager", "admin_api", "active", true)
	live["secretKey"], live["publishKey"] = "sec-SECRET", "pub-PUBLISH"
	f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON(live, keysetView("lk-2", "Retired", "sub-2", "consent", "pasted", "disabled", false)))
	human, js := runConnector(t, f, "", "keyset", "list"), runConnector(t, f, "", "keyset", "list", "--all", "--json")
	if strings.Contains(human.stdout, "Retired") || !strings.Contains(human.stdout, "sub-1") {
		t.Fatalf("list:\n%q", human.stdout)
	}
	for _, leak := range []string{"\x1b", "SECRET", "PUBLISH"} {
		if strings.Contains(human.stdout+js.stdout, leak) {
			t.Fatalf("%q reached the output:\n%q\n%q", leak, human.stdout, js.stdout)
		}
	}
	var got []connectors.LinkedKeyset
	if err := json.Unmarshal([]byte(js.stdout), &got); err != nil || len(got) != 2 || got[0].Label != "Prod\x1b[31m" {
		t.Fatalf("json (%v):\n%q", err, js.stdout)
	}
}

func TestConnectorKeysetDisableStatesItsReachAndDefaultsToNo(t *testing.T) {
	disableRoute := "POST " + connectorsAPI + "/keysets/lk-1/disable"
	f := newConnectorFake(t)
	f.reply(disableRoute, 200, mustJSON(keysetView("lk-1", "Prod", "sub-1", "access_manager", "admin_api", "disabled", true)))
	runConnector(t, f, "", "keyset", "disable", "Prod").wantErr(t, "--yes", "every org")
	f.wantUntouched(t, disableRoute)
	mustRun(t, f, "y\n", "keyset", "disable", "Prod").wantOut(t, "every Connector on it, in every org")
	if !f.requested(disableRoute) {
		t.Fatalf("keyset not disabled: %v", f.log)
	}
}

func TestConnectorKeysetLinkAndValidate(t *testing.T) {
	f := newConnectorFake(t)
	f.reply("POST "+connectorsAPI+"/keysets/lk-1/validate", 200, mustJSON(map[string]any{
		"id": "lk-1", "label": "Prod", "subscribeKey": "sub-1", "status": "active", "mode": "access_manager",
		"configChecked": false, "configSkippedReason": "no secret key"}))
	mustRun(t, f, "", "keyset", "link", "k-1", "--label", "My label")
	if got := f.body(linkRoute)["label"]; got != "My label" {
		t.Fatalf("link label = %v", got)
	}
	mustRun(t, f, "", "keyset", "validate", "Prod").wantOut(t, "no secret key")
	runConnector(t, f, "", "keyset", "validate", "nope").wantErr(t, "keyset list")
}
