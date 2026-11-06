package scaffold

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
)

// functionSource returns the `function name(...) { ... }` declaration from src, matching braces.
func functionSource(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "function "+name+"(")
	if start < 0 {
		t.Fatalf("generated app.js has no function %s", name)
	}
	depth := 0
	for i := strings.Index(src[start:], "{") + start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1]
			}
		}
	}
	t.Fatalf("unbalanced braces in function %s", name)
	return ""
}

// The page receives the SDK's AuthRequiredError, or the bare RPC error data from an older widget.
func TestWebappShowsConnectLinkForConnectionRefusal(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("BLOCKS_TEST_REQUIRE_NODE") != "" {
			t.Fatal("node not installed, and BLOCKS_TEST_REQUIRE_NODE is set")
		}
		t.Skip("node not installed")
	}
	card := loadFixtureCard(t, "echo2", "echo2", 200)
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, defaultVars())
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}
	harness := functionSource(t, out.AppJS, "connectUrlOf") + "\n" +
		functionSource(t, out.AppJS, "showConnectLink") + `
global.document = { createElement: (tag) => ({ tag }) };
const connect = 'https://app.blocks.ai/connect/org-1/google?agent=demo';
const cases = {
  sdkError: { name: 'AuthRequiredError', integration: 'google', connectUrl: connect, message: '[RPC] x' },
  rpcData: { message: '[RPC] x', data: { code: 'AuthRequired', details: { integration: 'google', connectUrl: connect } } },
  noService: { name: 'AuthRequiredError', connectUrl: connect },
  scriptUrl: { name: 'AuthRequiredError', integration: 'google', connectUrl: 'javascript:alert(1)' },
  scriptUrlInData: { data: { code: 'AuthRequired', details: { integration: 'google', connectUrl: 'javascript:alert(1)' } } },
  notAUrl: { name: 'AuthRequiredError', integration: 'google', connectUrl: 'not a url' },
  otherError: { message: 'boom', data: { code: 'InsufficientBalance', details: { connectUrl: connect } } },
};
const results = {};
for (const [name, err] of Object.entries(cases)) {
  const parts = [];
  const shown = showConnectLink(err, { append: (...p) => parts.push(...p) });
  const link = parts.find((p) => typeof p === 'object');
  results[name] = { shown, link: link || null, text: parts.filter((p) => typeof p === 'string').join('') };
}
console.log(JSON.stringify(results));
`
	cmd := exec.Command(node, "-e", harness)
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	type link struct{ Tag, Href, Target, Rel, TextContent string }
	var got map[string]struct {
		Shown bool
		Link  *link
		Text  string
	}
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	for _, name := range []string{"sdkError", "rpcData"} {
		r := got[name]
		if !r.Shown || r.Link == nil {
			t.Errorf("%s: no Connect link", name)
			continue
		}
		want := link{"a", "https://app.blocks.ai/connect/org-1/google?agent=demo", "_blank", "noopener noreferrer", "Connect google"}
		if *r.Link != want {
			t.Errorf("%s: link = %+v, want %+v", name, *r.Link, want)
		}
		if !strings.Contains(r.Text, "send again") {
			t.Errorf("%s: text %q does not say to send again", name, r.Text)
		}
	}
	if r := got["noService"]; r.Link == nil || r.Link.TextContent != "Connect the service" {
		t.Errorf("noService: link = %+v, want one labelled \"Connect the service\"", r.Link)
	}
	for _, name := range []string{"scriptUrl", "scriptUrlInData", "notAUrl", "otherError"} {
		if got[name].Shown {
			t.Errorf("%s: showed a Connect link, want the plain error message", name)
		}
	}
}
