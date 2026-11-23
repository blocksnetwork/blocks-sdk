package cmd

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
)

func TestConnectorCreateFallsBackToLinkedKeysetsWhenAccountsAreNotAvailable(t *testing.T) {
	f := newConnectorFake(t)
	f.replyError("GET "+connectorsAPI+"/account", 404, "NOT_FOUND", "Not found")
	if res := mustRun(t, f, "\n", args("create scores "+createFlagsLinked)...); len(res.browsed) != 0 {
		t.Fatalf("browsed %v", res.browsed)
	}
	f.wantCreatedOn(t, "lk-1")

	f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON())
	res := runConnector(t, f, "\n", args("create other "+createFlagsLinked)...)
	if want := "connecting a PubNub account is not available on this deployment; pass --keyset with a keyset already linked"; res.errorMsg != want {
		t.Fatalf("error = %v", res.err)
	}
}

func TestConnectorLinkReusesAKeysetLinkedFromThePubnubKeysetWithoutAccountRequests(t *testing.T) {
	for _, line := range []string{"create scores --pubnub-keyset k-1 " + createFlagsLinked, "keyset link k-1"} {
		f := newConnectorFake(t)
		linked := keysetView("lk-7", "Theirs", "sub-1", "access_manager", "pasted", "active", true)
		linked["pubnubKeysetId"] = "k-1"
		f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON(linked))
		mustRun(t, f, "", args(line)...).wantOut(t, `Keyset "Theirs" is already linked.`)
		f.wantUntouched(t, "/account", linkRoute)
	}
}

func TestConnectorCreateFailsBeforeLinkingWhenTheFlagsAlreadyDecideIt(t *testing.T) {
	bad, good := writeTemp(t, "{"), writeTemp(t, `{"type":"object"}`)
	pub := "create x --pubnub-keyset k-1 --kind publish --channel-template a.{b} "
	cases := []struct{ line, want string }{
		{"create x --kind subscribe --channel-template a.{b}", "--keyset"},
		{"create --keyset Prod --kind subscribe --channel-template a.{b}", "agent name"},
		{"create x --pubnub-keyset k-1 --channel-template a.{b}", "--kind"},
		{"create x --pubnub-keyset k-1 --kind video --channel-template a.{b}", "unknown kind"},
		{"create x --pubnub-keyset k-1 --kind subscribe --channel-template a.{b} --history-max 5", "History Connectors only"},
		{"create x --pubnub-keyset k-1 --kind history --channel-template a.{b} --history-max 101", "--history-max must be 1-100"},
		{"create x --pubnub-keyset k-1 --kind subscribe", "--channel-template"},
		{"create x --pubnub-keyset k-1 --kind publish --raw-channel foo --action-name go --input-schema " + good, "needs --channel-template"},
		{pub, "--action-name"},
		{pub + "--action-name go --input-schema /nonexistent.json", "cannot read"},
		{pub + "--action-name go --input-schema " + bad, "not valid JSON"},
		{pub + "--action-name go", "--input-schema"},
	}
	for _, tc := range cases {
		f := newConnectorFake(t)
		runConnector(t, f, "", args(tc.line)...).wantErr(t, tc.want)
		f.wantUntouched(t, linkRoute, createRoute, "/account")
	}
	f := newConnectorFake(t)
	runConnector(t, f, "\n", args("create x --pubnub-keyset k-1 --channel-template a.{b} --history-max 101")...).
		wantErr(t, "--history-max must be 1-100")
	f.wantUntouched(t, linkRoute, createRoute, "/account")
}

func TestConnectorCreateAsksOptionalValuesOnlyInWizardMode(t *testing.T) {
	f := newConnectorFake(t)
	res := mustRun(t, f, "\n", args("create scores --keyset Prod --kind history --channel-template live.{show} --history-max 50")...)
	if cfg, _ := f.body(createRoute)["config"].(map[string]any); cfg["historyMaxCount"] != float64(50) || strings.Contains(res.stdout, "enter to skip") {
		t.Fatalf("a terminal run with --kind asked a question or lost --history-max: %v\n%s", cfg, res.stdout)
	}
	mustRun(t, f, "\n\n\n", args("create other --keyset Prod --channel-template live.{show}")...).
		wantOut(t, "Display name (enter to skip)", "Description (enter to skip)")
}

func TestConnectorCreateReportsACancelledWaitPlainly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt cannot be sent to a process on Windows")
	}
	f := browserFake(t)
	f.onBrowser = func() {
		connectors.DefaultWaitTimeout = time.Minute
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
	}
	if res := runConnector(t, f, "\n", args("create scores "+createFlagsLinked)...); res.errorMsg != "stopped waiting; nothing was connected" {
		t.Fatalf("error = %v", res.err)
	}
}

func TestConnectorEditRawFlagsReplaceTheWholeSelector(t *testing.T) {
	f := exposuresFake(t, rawExposure([]string{"a"}, []string{"p.*"}))
	mustRun(t, f, "", "edit", "live", "--raw-pattern", "q.*")
	if got := mustJSON(f.body(patchRoute)["rawSelector"]); got != `{"patterns":["q.*"]}` {
		t.Fatalf("rawSelector = %s", got)
	}
	f = exposuresFake(t, exposureView("x1", "live", "subscribe", false)) // stored with a template
	mustRun(t, f, "", "edit", "live", "--raw-channel", "x")
	if got := mustJSON(f.body(patchRoute)["rawSelector"]); got != `{"channels":["x"]}` {
		t.Fatalf("rawSelector = %s", got)
	}
}
