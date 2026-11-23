package cmd

import (
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
)

const createFlagsLinked = "--kind subscribe --channel-template live.{show}"

func args(s string) []string { return strings.Fields(s) }

func consentFake(t *testing.T) *connectorFake {
	f := newConnectorFake(t)
	f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON(
		keysetView("lk-1", "Prod", "sub-1", "access_manager", "admin_api", "active", true),
		keysetView("lk-c", "legacy", "sub-c", "consent", "pasted", "active", false),
	))
	return f
}

func TestConnectorCreateWithLinkedKeysetNeedsNoAccountAndNoLink(t *testing.T) {
	f := consentFake(t)
	res := mustRun(t, f, "", args("create scores --keyset Prod --yes "+createFlagsLinked)...)
	f.wantUntouched(t, "/account", linkRoute)
	body := f.body(createRoute)
	if _, consent := body["consent"]; consent || body["orgId"] != "org-1" || body["linkedKeysetId"] != "lk-1" || body["agentName"] != "scores" || len(res.browsed) != 0 {
		t.Fatalf("create body %v, browsed %v", body, res.browsed)
	}
	res.wantOut(t, "[umbrella.blocks.ai / Engineering]", "Created Connector scores (Subscribe on live.{show}; all values: live.*)",
		"/agents/scores", "Callers of this agent can read messages")
}

func TestConnectorCreateConsent(t *testing.T) {
	cases := []struct{ stdin, extra, wantErr string }{
		{"", " --yes", "pass --consent to confirm"},
		{"", " --yes --consent", ""},
		{"\n", "", "consent was not given"}, // an empty answer is no
		{"y\n", "", ""},
	}
	for _, tc := range cases {
		f := consentFake(t)
		res := runConnector(t, f, tc.stdin, args("create chat --keyset legacy "+createFlagsLinked+tc.extra)...)
		res.wantOut(t, "has no Access Manager", "keeps the key after the Connector is revoked")
		if tc.wantErr != "" {
			res.wantErr(t, tc.wantErr)
			f.wantUntouched(t, createRoute)
		} else if f.body(createRoute)["consent"] != true {
			t.Fatalf("consent:true missing: %v", f.body(createRoute))
		}
	}
}

func TestConnectorCreateRefusesAKindTheKeysetDoesNotServe(t *testing.T) {
	f := consentFake(t)
	runConnector(t, f, "", args("create hist --keyset legacy --kind history --channel-template a.{b} --yes --consent")...).wantErr(t, "Message Persistence")
	f.wantUntouched(t, createRoute)
}

func TestConnectorCreateKeysetReuseOrder(t *testing.T) {
	line := args("create scores --pubnub-keyset k-1 " + createFlagsLinked)
	t.Run("another user's active connection is used without a browser and the keyset is linked", func(t *testing.T) {
		f := newConnectorFake(t)
		res := mustRun(t, f, "", line...)
		link := f.body(linkRoute)
		if link["source"] != "admin_api" || link["connectionId"] != "conn-1" || link["pubnubKeysetId"] != "k-1" || link["label"] != "Live / Prod" || len(res.browsed) != 0 {
			t.Fatalf("link body %v, browsed %v", link, res.browsed)
		}
		res.wantOut(t, `Using PubNub account "Acme PubNub", connected by Ana Lee.`)
	})
	t.Run("a picker row that is already linked is reused", func(t *testing.T) {
		f := newConnectorFake(t)
		f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON(keysetView("lk-2", "Staging", "sub-2", "access_manager", "admin_api", "active", true)))
		mustRun(t, f, "", args("create scores --pubnub-keyset k-2 --label ignored "+createFlagsLinked)...)
		f.wantUntouched(t, linkRoute)
		f.wantCreatedOn(t, "lk-2")
	})
	t.Run("a link that loses the race resolves to the existing keyset by subscribe key", func(t *testing.T) {
		f := newConnectorFake(t)
		f.replyError(linkRoute, 409, connectors.CodeKeysetAlreadyLinked, "already linked")
		f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON(keysetView("lk-9", "Theirs", "sub-1", "access_manager", "pasted", "active", true)))
		mustRun(t, f, "", line...)
		f.wantCreatedOn(t, "lk-9")
	})
}

func TestConnectorCreateOpensTheBrowserOnlyWithoutAnActiveConnection(t *testing.T) {
	f := browserFake(t)
	f.onBrowser = func() { // a colleague's connection showing up first must not end the wait
		f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON(
			connection("conn-2", "Theirs", "active", false), connection("conn-1", "Mine", "active", true)))
	}
	res := mustRun(t, f, "\n\n", args("create scores "+createFlagsLinked)...)
	if len(res.browsed) != 1 || !strings.Contains(res.browsed[0], "/manage/pubnub-connectors?connect=cli&org=org-1") {
		t.Fatalf("browsed = %v", res.browsed)
	}
	res.wantOut(t, "PubNub accounts tab", `connected: "Mine"`, "Linked", "Created Connector scores")
}

func TestConnectorCreateNeverOpensABrowserWhenNonInteractive(t *testing.T) {
	for stdin, extra := range map[string]string{"": "", "y\n": " --yes"} { // no terminal, and --yes in a terminal
		f := newConnectorFake(t)
		f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON())
		res := runConnector(t, f, stdin, args("create x --pubnub-keyset k-1 "+createFlagsLinked+extra)...)
		res.wantErr(t, "no PubNub account is connected in Engineering", "blocks connector account connect")
		if len(res.browsed) != 0 {
			t.Fatalf("browser opened: %v", res.browsed)
		}
	}
}

func TestConnectorCreateOffersToReconnectADeadOwnConnection(t *testing.T) {
	f := newConnectorFake(t)
	f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON(connection("conn-1", "Mine", "dead", true)))
	res := mustRun(t, f, "\n\n\n", args("create scores "+createFlagsLinked)...)
	res.wantOut(t, "no longer works. Reconnect it in the browser? [y/N]")
	if len(res.browsed) != 0 {
		t.Fatalf("declining (the default) opened the browser: %v", res.browsed)
	}
	f.wantCreatedOn(t, "lk-1")
}

func TestConnectorCreateWaitTimeoutStripsEscapesFromTheLastPollError(t *testing.T) {
	f := browserFake(t)
	f.onBrowser = func() { f.replyError("GET "+connectorsAPI+"/account", 503, "UNAVAILABLE", "\x1b[2Jdown") }
	res := runConnector(t, f, "\n", args("create scores "+createFlagsLinked)...)
	res.wantErr(t, connectors.TimeoutHint, "Engineering", "down")
	if strings.Contains(res.errorMsg, "\x1b") {
		t.Fatalf("escape sequence reached the terminal: %q", res.errorMsg)
	}
	f.wantUntouched(t, createRoute)
}

func TestConnectorCreateExplainsBackendRefusalsInOneLine(t *testing.T) {
	cases := []struct {
		status     int
		code, want string
	}{
		{409, "AgentNameTaken", "already taken"},
		{400, "PUBNUB_EXPOSURE_CONSENT_REQUIRED", "--consent"},
		{403, "PUBNUB_EXPOSURE_ORG_NOT_ALLOWED", "cannot be created in this org"},
		{403, "", "blocks login --org <org that holds the permission>"},
	}
	for _, tc := range cases {
		f := newConnectorFake(t)
		f.replyError(createRoute, tc.status, tc.code, `{"raw":"body"}`)
		res := runConnector(t, f, "", args("create x --keyset Prod --yes "+createFlagsLinked)...)
		res.wantErr(t, tc.want)
		if strings.ContainsAny(res.errorMsg, "{}\n") || strings.Contains(res.errorMsg, "raw") {
			t.Fatalf("error carries the response body or spans lines: %q", res.errorMsg)
		}
	}
}

func TestConnectorCreateStopsOnAnActiveConnectorWithTheName(t *testing.T) {
	f := newConnectorFake(t)
	f.reply("GET "+connectorsAPI+"/exposures", 200, exposuresJSON(exposureView("x1", "live_scores", "subscribe", false)))
	runConnector(t, f, "", args("create live_scores --keyset Prod --yes "+createFlagsLinked)...).
		wantErr(t, "the agent name live_scores is already taken. See `blocks connector list`")
	f.wantUntouched(t, "keysets", createRoute)
}

func TestConnectorCreateNeedsAnOrgIdFromTheProfile(t *testing.T) {
	f := newConnectorFake(t)
	t.Setenv(blocksAPIKeyEnv, "bk_not_in_profile")
	runConnector(t, f, "", args("create x --keyset Prod --yes "+createFlagsLinked)...).wantErr(t, "blocks login")
	f.wantUntouched(t, createRoute)
}

func TestConnectorCreatePublishSendsItsConfigAndJSONPrintsOnlyTheConnector(t *testing.T) {
	f := newConnectorFake(t)
	res := mustRun(t, f, "y\n", args("create ping --keyset Prod --kind publish --channel-template a.{b} --action-name send_ping "+
		"--custom-message-type ping --json --input-schema "+writeTemp(t, `{"type":"object"}`))...)
	cfg, _ := f.body(createRoute)["config"].(map[string]any)
	if schema, _ := cfg["inputSchema"].(map[string]any); cfg["actionName"] != "send_ping" || cfg["customMessageType"] != "ping" || schema["type"] != "object" {
		t.Fatalf("config = %v", cfg)
	}
	if !strings.HasPrefix(strings.TrimSpace(res.stdout), "{") || !strings.Contains(res.stdout, `"agentName": "ping"`) || strings.Contains(res.stdout, "Created Connector") {
		t.Fatalf("stdout:\n%s", res.stdout)
	}
}
