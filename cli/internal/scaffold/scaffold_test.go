package scaffold

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/schema"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

func pythonConfig() wizard.Config {
	return wizard.Config{
		Name:              "test_agent",
		Description:       "test_agent agent",
		Language:          "python",
		Mode:              "provider",
		Concurrency:       1,
		ExpectedInstances: 1,
		Streaming:         false,
		TaskKinds:         []string{"request"},
		Docker:            true,
	}
}

func nodeConfig() wizard.Config {
	cfg := pythonConfig()
	cfg.Language = "node"
	return cfg
}

func TestProjectPythonDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	expectedFiles := []string{
		"agent-card.json", "handler.py", "trigger.py", "pyproject.toml",
		".env", ".gitignore", "Dockerfile",
	}
	for _, f := range expectedFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected file %q to exist", f)
		}
	}

	// run.py should NOT be generated (blocks run replaces it)
	if _, err := os.Stat(filepath.Join(dir, "run.py")); err == nil {
		t.Error("run.py should NOT be generated; blocks run replaces it")
	}

	// pip.conf must NOT be generated; SDK is published to public PyPI
	if _, err := os.Stat(filepath.Join(dir, "pip.conf")); err == nil {
		t.Error("pip.conf should NOT be generated")
	}
}

func TestProjectNodeDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, nodeConfig(), nil); err != nil {
		t.Fatal(err)
	}

	expected := []string{"agent-card.json", "handler.ts", "trigger.ts", "package.json"}
	for _, f := range expected {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected file %q to exist", f)
		}
	}

	// main.ts should NOT be generated (blocks run replaces it)
	if _, err := os.Stat(filepath.Join(dir, "main.ts")); err == nil {
		t.Error("main.ts should NOT be generated; blocks run replaces it")
	}

	// .npmrc must NOT be generated; SDK is published to public npm
	if _, err := os.Stat(filepath.Join(dir, ".npmrc")); err == nil {
		t.Error(".npmrc should NOT be generated")
	}

	// No Python files should leak into a node project
	pythonFiles := []string{"handler.py", "pyproject.toml", "run.py"}
	for _, f := range pythonFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("Python file %q should not exist in node project", f)
		}
	}
}

func TestProjectNoDocker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	cfg := pythonConfig()
	cfg.Docker = false

	if err := Project(dir, cfg, nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err == nil {
		t.Error("Dockerfile should not exist when Docker is false")
	}
}

func TestProjectAgentCardContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "agent-card.json"))
	if err != nil {
		t.Fatal(err)
	}

	var card map[string]interface{}
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}

	// identity section
	identity, ok := card["identity"].(map[string]interface{})
	if !ok {
		t.Fatal("identity section missing or wrong type")
	}
	if identity["agentName"] != "test_agent" {
		t.Errorf("identity.agentName = %v, want %q", identity["agentName"], "test_agent")
	}
	if identity["displayName"] != "test_agent" {
		t.Errorf("identity.displayName = %v, want %q", identity["displayName"], "test_agent")
	}
	if identity["description"] != "test_agent agent" {
		t.Errorf("identity.description = %v, want %q", identity["description"], "test_agent agent")
	}
	if identity["version"] != "1.0.0" {
		t.Errorf("identity.version = %v, want %q", identity["version"], "1.0.0")
	}
	provider, ok := identity["provider"].(map[string]interface{})
	if !ok {
		t.Fatal("identity.provider section missing or wrong type")
	}
	if _, ok := provider["organization"]; !ok {
		t.Error("identity.provider.organization missing")
	}
	// identity.name must not exist (renamed to agentName/displayName)
	if _, exists := identity["name"]; exists {
		t.Error("identity.name should not exist; use agentName and displayName")
	}

	// capabilities section
	caps, ok := card["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatal("capabilities section missing or wrong type")
	}
	taskKinds, ok := caps["taskKinds"].([]interface{})
	if !ok {
		t.Fatal("capabilities.taskKinds missing or wrong type")
	}
	if len(taskKinds) != 1 || taskKinds[0] != "request" {
		t.Errorf("capabilities.taskKinds = %v, want [request]", taskKinds)
	}

	// io section
	ioSection, ok := card["io"].(map[string]interface{})
	if !ok {
		t.Fatal("io section missing or wrong type")
	}
	inputs, ok := ioSection["inputs"].([]interface{})
	if !ok || len(inputs) < 1 {
		t.Fatal("io.inputs missing or empty")
	}
	input0, ok := inputs[0].(map[string]interface{})
	if !ok {
		t.Fatal("io.inputs[0] wrong type")
	}
	if input0["id"] != "request" {
		t.Errorf("io.inputs[0].id = %v, want %q", input0["id"], "request")
	}
	if input0["contentType"] != "application/json" {
		t.Errorf("io.inputs[0].contentType = %v, want %q", input0["contentType"], "application/json")
	}

	outputs, ok := ioSection["outputs"].([]interface{})
	if !ok || len(outputs) < 1 {
		t.Fatal("io.outputs missing or empty")
	}
	output0, ok := outputs[0].(map[string]interface{})
	if !ok {
		t.Fatal("io.outputs[0] wrong type")
	}
	if output0["id"] != "result" {
		t.Errorf("io.outputs[0].id = %v, want %q", output0["id"], "result")
	}

	// Removed fields must not be present
	if _, exists := card["name"]; exists {
		t.Error("top-level 'name' should not exist in new format")
	}
	if _, exists := card["defaultInputModes"]; exists {
		t.Error("defaultInputModes should not exist in new format")
	}
	if _, exists := card["defaultOutputModes"]; exists {
		t.Error("defaultOutputModes should not exist in new format")
	}

	// runtime section
	rt, ok := card["runtime"].(map[string]interface{})
	if !ok {
		t.Fatal("runtime section missing or wrong type")
	}
	if rt["concurrency"].(float64) != 1 {
		t.Errorf("concurrency = %v, want 1", rt["concurrency"])
	}
	if rt["handler"] != "./handler.py" {
		t.Errorf("handler = %v, want %q", rt["handler"], "./handler.py")
	}
	// runtime.agentName must not exist (lives in identity.agentName)
	if _, exists := rt["agentName"]; exists {
		t.Error("runtime.agentName should not exist; use identity.agentName")
	}
}

func TestProjectAgentCardStreaming(t *testing.T) {
	cfg := pythonConfig()
	cfg.Streaming = true
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, cfg, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "agent-card.json"))
	if err != nil {
		t.Fatal(err)
	}

	var card map[string]interface{}
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}

	streams, ok := card["streams"].(map[string]interface{})
	if !ok {
		t.Fatal("streams section missing when streaming is enabled")
	}
	defaultStream, ok := streams["_default"].(map[string]interface{})
	if !ok {
		t.Fatal("streams._default missing")
	}
	if defaultStream["direction"] != "outbound" {
		t.Errorf("streams._default.direction = %v, want %q", defaultStream["direction"], "outbound")
	}
	if defaultStream["format"] != "events" {
		t.Errorf("streams._default.format = %v, want %q", defaultStream["format"], "events")
	}
}

func TestProjectAgentCardStreamingPipeOnly(t *testing.T) {
	cfg := pythonConfig()
	cfg.Streaming = true
	cfg.TaskKinds = []string{"pipe"}
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, cfg, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "agent-card.json"))
	if err != nil {
		t.Fatal(err)
	}

	var card map[string]interface{}
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}

	streams, ok := card["streams"].(map[string]interface{})
	if !ok {
		t.Fatal("streams section missing when streaming is enabled for pipe-only agent")
	}

	// Must NOT contain _default
	if _, exists := streams["_default"]; exists {
		t.Error("pipe-only agent should not generate _default stream")
	}

	// Must contain named "stream" with dedicated affinity
	namedStream, ok := streams["stream"].(map[string]interface{})
	if !ok {
		t.Fatal(`streams["stream"] missing for pipe-only agent`)
	}
	if namedStream["direction"] != "outbound" {
		t.Errorf(`streams["stream"].direction = %v, want "outbound"`, namedStream["direction"])
	}
	if namedStream["format"] != "events" {
		t.Errorf(`streams["stream"].format = %v, want "events"`, namedStream["format"])
	}
	if namedStream["affinity"] != "dedicated" {
		t.Errorf(`streams["stream"].affinity = %v, want "dedicated"`, namedStream["affinity"])
	}

	// Validate the generated card passes schema validation
	res := schema.Validate(filepath.Join(dir, "agent-card.json"))
	if len(res.Errors) > 0 {
		t.Errorf("pipe-only streaming card should pass validation, got errors: %v", res.Errors)
	}
}

func TestProjectAgentCardStreamingRequestPipe(t *testing.T) {
	cfg := pythonConfig()
	cfg.Streaming = true
	cfg.TaskKinds = []string{"request", "pipe"}
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, cfg, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "agent-card.json"))
	if err != nil {
		t.Fatal(err)
	}

	var card map[string]interface{}
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}

	streams, ok := card["streams"].(map[string]interface{})
	if !ok {
		t.Fatal("streams section missing when streaming is enabled for request+pipe agent")
	}

	// When request is included, _default should be generated
	defaultStream, ok := streams["_default"].(map[string]interface{})
	if !ok {
		t.Fatal("streams._default missing for request+pipe agent")
	}
	if defaultStream["direction"] != "outbound" {
		t.Errorf("streams._default.direction = %v, want %q", defaultStream["direction"], "outbound")
	}

	// _default must not have affinity:shared (schema rule)
	if aff, exists := defaultStream["affinity"]; exists && aff == "shared" {
		t.Error("_default stream must not have affinity:shared")
	}
}

func TestProjectAgentCardNoStreamingByDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "agent-card.json"))
	if err != nil {
		t.Fatal(err)
	}

	var card map[string]interface{}
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}

	if _, exists := card["streams"]; exists {
		t.Error("streams section should not exist when streaming is disabled")
	}
}

func TestProjectAgentCardTaskKinds(t *testing.T) {
	cfg := pythonConfig()
	cfg.TaskKinds = []string{"request", "pipe"}
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, cfg, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "agent-card.json"))
	if err != nil {
		t.Fatal(err)
	}

	var card map[string]interface{}
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}

	caps := card["capabilities"].(map[string]interface{})
	taskKinds := caps["taskKinds"].([]interface{})
	if len(taskKinds) != 2 {
		t.Errorf("capabilities.taskKinds length = %d, want 2", len(taskKinds))
	}
	if taskKinds[0] != "request" || taskKinds[1] != "pipe" {
		t.Errorf("capabilities.taskKinds = %v, want [request, pipe]", taskKinds)
	}
}

func TestProjectNodeAgentCardHandler(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	cfg := nodeConfig()
	cfg.TaskKinds = []string{"request"}
	if err := Project(dir, cfg, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "agent-card.json"))
	if err != nil {
		t.Fatal(err)
	}

	var card map[string]interface{}
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}

	// Verify identity section for node
	identity := card["identity"].(map[string]interface{})
	if identity["agentName"] != "test_agent" {
		t.Errorf("identity.agentName = %v, want %q", identity["agentName"], "test_agent")
	}
	if identity["displayName"] != "test_agent" {
		t.Errorf("identity.displayName = %v, want %q", identity["displayName"], "test_agent")
	}

	rt := card["runtime"].(map[string]interface{})
	if rt["handler"] != "./handler.ts" {
		t.Errorf("handler = %v, want %q", rt["handler"], "./handler.ts")
	}
	if rt["handlerExport"] != "default" {
		t.Errorf("handlerExport = %v, want %q", rt["handlerExport"], "default")
	}
}

func TestProjectPackageJSONContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, nodeConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}

	var pkg map[string]interface{}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}

	if pkg["name"] != "test_agent" {
		t.Errorf("name = %v, want %q", pkg["name"], "test_agent")
	}
	if pkg["type"] != "module" {
		t.Errorf("type = %v, want %q", pkg["type"], "module")
	}

	deps, ok := pkg["dependencies"].(map[string]interface{})
	if !ok {
		t.Fatal("dependencies missing or wrong type")
	}
	if _, ok := deps["@blocks-network/sdk"]; !ok {
		t.Error("missing @blocks-network/sdk dependency")
	}

	// tsx should NOT be a dependency (blocks-run replaces tsx main.ts)
	if _, ok := deps["tsx"]; ok {
		t.Error("tsx should not be a dependency; blocks-run replaces tsx main.ts")
	}

	// Verify start script delegates to blocks run
	scripts, ok := pkg["scripts"].(map[string]interface{})
	if !ok {
		t.Fatal("scripts missing or wrong type")
	}
	if scripts["start"] != "blocks run" {
		t.Errorf("scripts.start = %v, want %q", scripts["start"], "blocks run")
	}
	if scripts["check"] != "blocks check" {
		t.Errorf("scripts.check = %v, want %q", scripts["check"], "blocks check")
	}
}

func TestProjectNodeNoMainTS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, nodeConfig(), nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "main.ts")); err == nil {
		t.Error("main.ts should NOT exist for Node projects; blocks run replaces it")
	}
}

func TestProjectPythonNoRunPy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "run.py")); err == nil {
		t.Error("run.py should NOT exist for Python projects; blocks run replaces it")
	}
}

func TestProjectPyprojectContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, `name = "test_agent"`) {
		t.Error("pyproject.toml should contain project name")
	}
	if !strings.Contains(content, "blocks-network") {
		t.Error("pyproject.toml should contain blocks-network dependency")
	}
	if !strings.Contains(content, ">=3.12") {
		t.Error("pyproject.toml should require Python >= 3.12")
	}
	// run module should not be listed since run.py is no longer generated
	if strings.Contains(content, `"run"`) {
		t.Error("pyproject.toml should not list 'run' module; run.py is no longer generated")
	}
}

func TestProjectEnvContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Should contain CDM-only vars, not PUBNUB_* vars
	if strings.Contains(content, "PUBNUB_PUBLISH_KEY") {
		t.Error(".env should not contain PUBNUB_PUBLISH_KEY")
	}
	if strings.Contains(content, "PUBNUB_SUBSCRIBE_KEY") {
		t.Error(".env should not contain PUBNUB_SUBSCRIBE_KEY")
	}
	if strings.Contains(content, "PUBNUB_SECRET_KEY") {
		t.Error(".env should not contain PUBNUB_SECRET_KEY")
	}
	if strings.Contains(content, "BLOCKS_CDM_URL") {
		t.Error(".env should not contain BLOCKS_CDM_URL (defaults to production CDN)")
	}
	if !strings.Contains(content, "BLOCKS_API_KEY") {
		t.Error(".env should contain BLOCKS_API_KEY")
	}
}

func TestNodeTriggerNoPubNub(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, nodeConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "trigger.ts"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, "PUBNUB_SUBSCRIBE_KEY") {
		t.Error("trigger.ts should not reference PUBNUB_SUBSCRIBE_KEY")
	}
	if strings.Contains(content, "PUBNUB_PUBLISH_KEY") {
		t.Error("trigger.ts should not reference PUBNUB_PUBLISH_KEY")
	}
	if strings.Contains(content, "createPubNubClient") {
		t.Error("trigger.ts should not reference createPubNubClient")
	}
	if strings.Contains(content, "subscribeKey") {
		t.Error("trigger.ts should not expose subscribeKey")
	}
	if strings.Contains(content, "AgentAuth") {
		t.Error("trigger.ts should not expose AgentAuth")
	}
	if !strings.Contains(content, "TaskClient.create") {
		t.Error("trigger.ts should use TaskClient.create() factory")
	}
}

func TestPythonTriggerNoPubNub(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "trigger.py"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, "PUBNUB_SUBSCRIBE_KEY") {
		t.Error("trigger.py should not reference PUBNUB_SUBSCRIBE_KEY")
	}
	if strings.Contains(content, "PUBNUB_PUBLISH_KEY") {
		t.Error("trigger.py should not reference PUBNUB_PUBLISH_KEY")
	}
	if strings.Contains(content, "create_pubnub_client") {
		t.Error("trigger.py should not reference create_pubnub_client")
	}
	if strings.Contains(content, "subscribe_key") {
		t.Error("trigger.py should not expose subscribe_key")
	}
	if strings.Contains(content, "AgentAuth") {
		t.Error("trigger.py should not expose AgentAuth")
	}
	if !strings.Contains(content, "create_task_client") {
		t.Error("trigger.py should use create_task_client() from client module")
	}
}

func TestNodeHandlerArtifactsShape(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, nodeConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "handler.ts"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "artifacts:") {
		t.Error("handler.ts should return artifacts (plural) array")
	}
	// Ensure the old singular pattern is not present as a return key
	if strings.Contains(content, "artifact:") && !strings.Contains(content, "artifacts:") {
		t.Error("handler.ts should not return singular 'artifact:'; use 'artifacts:' array")
	}
}

func TestPythonHandlerArtifactsShape(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "handler.py"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, `"artifacts"`) {
		t.Error("handler.py should return 'artifacts' (plural) key")
	}
	if strings.Contains(content, `"artifact":`) {
		t.Error("handler.py should not return singular 'artifact:'; use 'artifacts' array")
	}
}

func TestPythonTriggerKeywordArgs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "trigger.py"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, "SendMessageParams") {
		t.Error("trigger.py should not wrap args in SendMessageParams; use keyword args directly")
	}
	if !strings.Contains(content, "agent_name=") {
		t.Error("trigger.py should pass agent_name as keyword arg to send_message()")
	}
	if !strings.Contains(content, "client.send_message(") {
		t.Error("trigger.py should call client.send_message()")
	}
}

func TestNodeTriggerTypedEvents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, nodeConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "trigger.ts"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "ProgressEvent") {
		t.Error("trigger.ts should use typed ProgressEvent")
	}
	if !strings.Contains(content, "ArtifactEvent") {
		t.Error("trigger.ts should use typed ArtifactEvent")
	}
	if !strings.Contains(content, "TerminalEvent") {
		t.Error("trigger.ts should use typed TerminalEvent")
	}
}

func TestTriggerUsesPartHelpers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent_node")
	if err := Project(dir, nodeConfig(), nil); err != nil {
		t.Fatal(err)
	}

	nodeData, err := os.ReadFile(filepath.Join(dir, "trigger.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nodeData), "textPart(") {
		t.Error("trigger.ts should use textPart() helper for request parts")
	}

	dir2 := filepath.Join(t.TempDir(), "test_agent_py")
	if err := Project(dir2, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	pyData, err := os.ReadFile(filepath.Join(dir2, "trigger.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pyData), "SendMessageRequestPart(") {
		t.Error("trigger.py should use SendMessageRequestPart() for request parts")
	}
}

func TestProjectGeneratedCardPassesValidation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	cardPath := filepath.Join(dir, "agent-card.json")
	res := schema.Validate(cardPath)

	if len(res.Errors) > 0 {
		t.Errorf("generated card should pass validation, got errors: %v", res.Errors)
	}
}

func TestPythonNoClientPy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "client.py")); err == nil {
		t.Error("client.py should not be generated; create_task_client lives in the SDK")
	}
}

func TestPythonTriggerImportsFromSDK(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "test_agent")
	if err := Project(dir, pythonConfig(), nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "trigger.py"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, "from blocks_network import") {
		t.Error("trigger.py should import create_task_client from blocks_network")
	}
	if !strings.Contains(content, "create_task_client") {
		t.Error("trigger.py should use create_task_client()")
	}
	if strings.Contains(content, "from client import") {
		t.Error("trigger.py should not import from local client module; use blocks_network SDK")
	}
	if strings.Contains(content, "os.environ") {
		t.Error("trigger.py should not access os.environ directly; use create_task_client()")
	}
	if strings.Contains(content, "load_dotenv") {
		t.Error("trigger.py should not call load_dotenv(); create_task_client() handles it")
	}
}

func TestProjectRejectsUnknownLanguage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x")
	cfg := pythonConfig()
	cfg.Language = "rust"
	err := Project(dir, cfg, nil)
	if err == nil {
		t.Fatal("Project should reject unknown Language")
	}
	if !strings.Contains(err.Error(), "unsupported language") {
		t.Errorf("error = %q, want 'unsupported language' wording", err.Error())
	}
}

func TestProjectRejectsUnknownMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "x")
	cfg := pythonConfig()
	cfg.Mode = "not-a-real-mode"
	err := Project(dir, cfg, nil)
	if err == nil {
		t.Fatal("Project should reject unknown Mode")
	}
	if !strings.Contains(err.Error(), "unsupported mode") {
		t.Errorf("error = %q, want 'unsupported mode' wording", err.Error())
	}
}

func nodeConsumerConfig() wizard.Config {
	return wizard.Config{
		Name:        "my_consumer",
		Description: "my_consumer consumer",
		Language:    "node",
		Mode:        "consumer",
	}
}

func TestProjectConsumerNodeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	if err := Project(dir, nodeConsumerConfig(), nil); err != nil {
		t.Fatal(err)
	}
	expected := []string{"index.ts", "package.json", ".env", ".gitignore"}
	for _, f := range expected {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected file %q to exist", f)
		}
	}
	forbidden := []string{"agent-card.json", "handler.ts", "trigger.ts", "Dockerfile", ".npmrc"}
	for _, f := range forbidden {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("consumer project should not contain %q", f)
		}
	}
}

func TestConsumerNodeIndexContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	if err := Project(dir, nodeConsumerConfig(), nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	required := []string{
		"TaskClient.create",
		"sendMessage",
		"waitForTerminal",
		"listArtifacts",
		"BLOCKS_API_KEY",
		"process.exit",
		"terminal.state === 'completed'",
	}
	for _, needle := range required {
		if !strings.Contains(content, needle) {
			t.Errorf("index.ts should contain %q", needle)
		}
	}
	forbidden := []string{"StartTaskMessage", "HandlerResult"}
	for _, needle := range forbidden {
		if strings.Contains(content, needle) {
			t.Errorf("index.ts should not contain %q", needle)
		}
	}
}

func TestConsumerNodePackageScripts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	if err := Project(dir, nodeConsumerConfig(), nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg map[string]interface{}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatal(err)
	}
	scripts := pkg["scripts"].(map[string]interface{})
	if scripts["start"] != "tsx index.ts" {
		t.Errorf("scripts.start = %v, want %q", scripts["start"], "tsx index.ts")
	}
	if _, exists := scripts["check"]; exists {
		t.Error("consumer package.json should not contain 'check' script")
	}
	devDeps := pkg["devDependencies"].(map[string]interface{})
	if _, ok := devDeps["tsx"]; !ok {
		t.Error("consumer devDependencies should include tsx")
	}
}

func TestConsumerNoAgentCard(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	if err := Project(dir, nodeConsumerConfig(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent-card.json")); err == nil {
		t.Error("consumer project should not contain agent-card.json")
	}
}

func pythonConsumerConfig() wizard.Config {
	return wizard.Config{
		Name:        "my_consumer",
		Description: "my_consumer consumer",
		Language:    "python",
		Mode:        "consumer",
	}
}

func TestProjectConsumerPythonFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	if err := Project(dir, pythonConsumerConfig(), nil); err != nil {
		t.Fatal(err)
	}
	expected := []string{"main.py", "pyproject.toml", ".env", ".gitignore"}
	for _, f := range expected {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected file %q to exist", f)
		}
	}
	forbidden := []string{"agent-card.json", "handler.py", "trigger.py", "Dockerfile", "pip.conf"}
	for _, f := range forbidden {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("consumer project should not contain %q", f)
		}
	}
}

func TestConsumerPythonMainContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	if err := Project(dir, pythonConsumerConfig(), nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "main.py"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	required := []string{
		"TaskClient.create",
		"client.send_message",
		"wait_for_terminal",
		"list_artifacts",
		"BLOCKS_API_KEY",
		"sys.exit",
		"load_dotenv",
		`terminal.state != "completed"`,
	}
	for _, needle := range required {
		if !strings.Contains(content, needle) {
			t.Errorf("main.py should contain %q", needle)
		}
	}
	forbidden := []string{"StartTaskMessage", "create_task_client"}
	for _, needle := range forbidden {
		if strings.Contains(content, needle) {
			t.Errorf("main.py should not contain %q (consumer uses explicit guard, not create_task_client)", needle)
		}
	}
}

func TestConsumerPythonPyprojectContent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	if err := Project(dir, pythonConsumerConfig(), nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, `py-modules = ["main"]`) {
		t.Error("consumer pyproject.toml should list py-modules = [\"main\"]")
	}
	if strings.Contains(content, `py-modules = ["handler"]`) {
		t.Error("consumer pyproject.toml should not list handler module")
	}
	if !strings.Contains(content, "python-dotenv") {
		t.Error("consumer pyproject.toml should depend on python-dotenv")
	}
	if !strings.Contains(content, "blocks-network") {
		t.Error("consumer pyproject.toml should depend on blocks-network")
	}
}
