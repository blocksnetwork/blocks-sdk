package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/spf13/cobra"
)

type connectorShape struct {
	channelTemplate   string
	rawChannels       []string
	rawPatterns       []string
	displayName       string
	description       string
	historyMax        int
	historySet        bool
	actionName        string
	customMessageType string
	inputSchema       string
}

func addShapeFlags(cmd *cobra.Command, s *connectorShape) {
	f := cmd.Flags()
	f.StringVar(&s.channelTemplate, "channel-template", "", "Channel Template, for example 'live.{show}'")
	f.StringArrayVar(&s.rawChannels, "raw-channel", nil, "Raw access: a channel callers may use (repeatable)")
	f.StringArrayVar(&s.rawPatterns, "raw-pattern", nil, "Raw access: a channel pattern callers may use (repeatable)")
	f.StringVar(&s.displayName, "display-name", "", "Display name")
	f.StringVar(&s.description, "description", "", "Description")
	f.IntVar(&s.historyMax, "history-max", 0, "History: most messages a caller may fetch (1-100)")
	f.StringVar(&s.actionName, "action-name", "", "Publish: name of the message callers send")
	f.StringVar(&s.customMessageType, "custom-message-type", "", "Publish: custom message type")
	f.StringVar(&s.inputSchema, "input-schema", "", "Publish: JSON Schema file for the message")
	cmd.MarkFlagsMutuallyExclusive("channel-template", "raw-channel")
	cmd.MarkFlagsMutuallyExclusive("channel-template", "raw-pattern")
}

func (s *connectorShape) captureChanged(cmd *cobra.Command) {
	s.historySet = cmd.Flags().Changed("history-max")
}

func (s connectorShape) raw() bool { return len(s.rawChannels) > 0 || len(s.rawPatterns) > 0 }

func (s connectorShape) rawSelector() *connectors.RawSelector {
	return &connectors.RawSelector{Channels: s.rawChannels, Patterns: s.rawPatterns}
}

func (s connectorShape) checkKind(kind string) error {
	if s.historySet && kind != connectors.KindHistory {
		return errors.New("--history-max applies to History Connectors only")
	}
	if s.raw() && kind == connectors.KindPublish {
		return errors.New("a Publish Connector needs --channel-template: raw channels are not available for Publish")
	}
	for _, f := range []struct{ name, value string }{
		{"--action-name", s.actionName},
		{"--custom-message-type", s.customMessageType},
		{"--input-schema", s.inputSchema},
	} {
		if f.value != "" && kind != connectors.KindPublish {
			return fmt.Errorf("%s applies to Publish Connectors only", f.name)
		}
	}
	return nil
}

func (s connectorShape) configChanged() bool {
	return s.historySet || s.actionName != "" || s.customMessageType != "" || s.inputSchema != ""
}

// config applies the flags to base; the backend replaces config whole, so edit passes the stored one.
func (s connectorShape) config(base map[string]any) (map[string]any, error) {
	cfg := make(map[string]any, len(base)+3)
	for k, v := range base {
		cfg[k] = v
	}
	if s.historySet {
		if s.historyMax < 1 || s.historyMax > connectors.MaxHistoryCount {
			return nil, fmt.Errorf("--history-max must be 1-%d", connectors.MaxHistoryCount)
		}
		cfg["historyMaxCount"] = s.historyMax
	}
	if s.actionName != "" {
		cfg["actionName"] = s.actionName
	}
	if s.customMessageType != "" {
		cfg["customMessageType"] = s.customMessageType
	}
	if s.inputSchema != "" {
		schema, err := readInputSchema(s.inputSchema)
		if err != nil {
			return nil, err
		}
		cfg["inputSchema"] = schema
	}
	return cfg, nil
}

func readInputSchema(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--input-schema: cannot read %s", termsafe.Text(path))
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("--input-schema: %s is not valid JSON", termsafe.Text(path))
	}
	return json.RawMessage(data), nil
}

func (e *connectorEnv) requireText(value, label, flag string) (string, error) {
	if value != "" {
		return value, nil
	}
	if !e.interactive {
		return "", fmt.Errorf("%s is required: pass %s", label, flag)
	}
	for {
		answer, err := e.promptLine(label)
		if err != nil {
			return "", err
		}
		if answer != "" {
			return answer, nil
		}
	}
}

func (e *connectorEnv) optionalText(value, label string) (string, error) {
	if value != "" || !e.wizard {
		return value, nil
	}
	return e.promptLine(label + " (enter to skip)")
}

const (
	templateLabel = "Channel Template"
	templateFlag  = "--channel-template (or --raw-channel / --raw-pattern)"
)

func (e *connectorEnv) checkBeforeKeyset(kind string, s connectorShape) error {
	if err := s.checkKind(kind); err != nil {
		return err
	}
	if !e.interactive {
		if err := e.requiredShape(kind, &s); err != nil {
			return err
		}
	}
	_, err := s.config(nil)
	return err
}

func (e *connectorEnv) requiredShape(kind string, s *connectorShape) error {
	var err error
	if !s.raw() {
		if s.channelTemplate, err = e.requireText(s.channelTemplate, templateLabel, templateFlag); err != nil {
			return err
		}
	}
	if kind == connectors.KindPublish {
		if s.actionName, err = e.requireText(s.actionName, "Action name", "--action-name"); err != nil {
			return err
		}
		if s.inputSchema, err = e.requireText(s.inputSchema, "Input schema file", "--input-schema"); err != nil {
			return err
		}
	}
	return nil
}

func (e *connectorEnv) fillShape(kind string, s *connectorShape) error {
	if err := e.requiredShape(kind, s); err != nil {
		return err
	}
	var err error
	if s.displayName, err = e.optionalText(s.displayName, "Display name"); err != nil {
		return err
	}
	if s.description, err = e.optionalText(s.description, "Description"); err != nil {
		return err
	}
	switch kind {
	case connectors.KindHistory:
		return e.fillHistory(s)
	case connectors.KindPublish:
		return e.fillPublish(s)
	}
	return nil
}

func (e *connectorEnv) fillHistory(s *connectorShape) error {
	if s.historySet || !e.wizard {
		return nil
	}
	answer, err := e.promptLine("Most messages a caller may fetch, 1-100 (enter for the default)")
	if err != nil || answer == "" {
		return err
	}
	n, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil {
		return fmt.Errorf("%q is not a number", termsafe.Text(answer))
	}
	s.historyMax, s.historySet = n, true
	return nil
}

func (e *connectorEnv) fillPublish(s *connectorShape) error {
	var err error
	s.customMessageType, err = e.optionalText(s.customMessageType, "Custom message type")
	return err
}
