// Package cardfetch fetches an agent card from the Blocks registry for use
// by the embed scaffold generator. It is intentionally narrow: a single
// Fetch entry point, a typed AgentCard struct that names only the card
// fields the generator reads, and a typed sentinel for the not-found case.
//
// The package depends only on internal/blocksapi and the standard library;
// no auth, no environment lookups, no scaffold knowledge. Callers in cmd/
// resolve the backend URL and credentials, build the *blocksapi.Client,
// then invoke Fetch.
package cardfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

// ErrAgentNotFound is returned by Fetch when the registry responds with
// HTTP 404 for the requested agentName. Callers compare via errors.Is.
var ErrAgentNotFound = errors.New("agent not found")

// AgentCard is the subset of card fields the embed generator consumes.
// All other fields in the registry response (pricing, ownership, runtime,
// embeddedAuth, etc.) are ignored.
type AgentCard struct {
	AgentName string
	TaskKinds []string
	Inputs    []InputDecl
	Outputs   []OutputDecl
	// Streams is keyed by the card-level stream key (e.g. "_default").
	// Empty or absent in the card → empty map.
	Streams map[string]StreamDecl
}

// InputDecl mirrors a single io.inputs[] entry. Schema and Example are
// retained as raw JSON so the generator can re-emit them verbatim.
type InputDecl struct {
	ID          string
	Description string
	ContentType string
	Required    bool
	Schema      json.RawMessage
	Example     json.RawMessage
}

// OutputDecl mirrors a single io.outputs[] entry.
type OutputDecl struct {
	ID          string
	Description string
	ContentType string
	Guaranteed  bool
}

// StreamDecl mirrors one entry under the streams object.
//
// Direction is "outbound" | "inbound" | "bidirectional".
// Format is "events" | "bytes".
// Affinity is "dedicated" | "shared" | "" (omitted).
type StreamDecl struct {
	Direction   string
	Format      string
	Description string
	Affinity    string
}

// outerEnvelope is the wrapped response shape from
// GET /api/v1/registry/agents?agentName=<name>.
type outerEnvelope struct {
	Agent struct {
		AgentName string          `json:"agentName"`
		Card      json.RawMessage `json:"card"`
	} `json:"agent"`
}

// cardJSON parses agent.card into the subset the generator uses.
type cardJSON struct {
	Capabilities struct {
		TaskKinds []string `json:"taskKinds"`
	} `json:"capabilities"`
	IO *struct {
		Inputs  []inputJSON  `json:"inputs"`
		Outputs []outputJSON `json:"outputs"`
	} `json:"io"`
	Streams map[string]streamJSON `json:"streams"`
}

type inputJSON struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	ContentType string          `json:"contentType"`
	Required    bool            `json:"required"`
	Schema      json.RawMessage `json:"schema"`
	Example     json.RawMessage `json:"example"`
}

type outputJSON struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	ContentType string `json:"contentType"`
	Guaranteed  bool   `json:"guaranteed"`
}

type streamJSON struct {
	Direction   string `json:"direction"`
	Format      string `json:"format"`
	Description string `json:"description"`
	Affinity    string `json:"affinity"`
}

// Fetch performs GET /api/v1/registry/agents?agentName=<agentName> via the
// shared blocksapi client and returns the parsed AgentCard.
//
// Errors:
//   - 404 → wraps ErrAgentNotFound (callers can errors.Is(err, ErrAgentNotFound)).
//   - non-2xx (other) → wrapped *blocksapi.APIError.
//   - envelope mismatch (agent.agentName != requested) → typed error.
//   - parse / network failures → wrapped error.
func Fetch(ctx context.Context, client *blocksapi.Client, agentName string) (*AgentCard, error) {
	if client == nil {
		return nil, fmt.Errorf("cardfetch.Fetch: nil client")
	}
	if agentName == "" {
		return nil, fmt.Errorf("cardfetch.Fetch: empty agentName")
	}

	q := url.Values{"agentName": []string{agentName}}
	resp, err := client.Get(ctx, "/api/v1/registry/agents", q)
	if err != nil {
		var apiErr *blocksapi.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return nil, fmt.Errorf("agent %q: %w", agentName, ErrAgentNotFound)
		}
		return nil, fmt.Errorf("fetch agent %q: %w", agentName, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read agent %q response: %w", agentName, err)
	}

	var envelope outerEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("parse agent %q envelope: %w", agentName, err)
	}

	if envelope.Agent.AgentName == "" {
		return nil, fmt.Errorf("registry returned empty envelope for %q", agentName)
	}
	if envelope.Agent.AgentName != agentName {
		// Defensive: catches URL-encoding bugs and registry quirks before
		// the mismatched card poisons the scaffold.
		return nil, fmt.Errorf(
			"registry returned card for %q but we asked for %q",
			envelope.Agent.AgentName, agentName,
		)
	}

	if len(envelope.Agent.Card) == 0 {
		return nil, fmt.Errorf("agent %q: registry response missing card", agentName)
	}

	var card cardJSON
	if err := json.Unmarshal(envelope.Agent.Card, &card); err != nil {
		return nil, fmt.Errorf("parse agent %q card: %w", agentName, err)
	}

	return buildAgentCard(envelope.Agent.AgentName, &card), nil
}

func buildAgentCard(name string, c *cardJSON) *AgentCard {
	out := &AgentCard{
		AgentName: name,
		TaskKinds: append([]string(nil), c.Capabilities.TaskKinds...),
		Streams:   map[string]StreamDecl{},
	}
	if c.IO != nil {
		out.Inputs = make([]InputDecl, 0, len(c.IO.Inputs))
		for _, in := range c.IO.Inputs {
			out.Inputs = append(out.Inputs, InputDecl{
				ID:          in.ID,
				Description: in.Description,
				ContentType: in.ContentType,
				Required:    in.Required,
				Schema:      cloneRaw(in.Schema),
				Example:     cloneRaw(in.Example),
			})
		}
		out.Outputs = make([]OutputDecl, 0, len(c.IO.Outputs))
		for _, op := range c.IO.Outputs {
			out.Outputs = append(out.Outputs, OutputDecl{
				ID:          op.ID,
				Description: op.Description,
				ContentType: op.ContentType,
				Guaranteed:  op.Guaranteed,
			})
		}
	}
	for k, s := range c.Streams {
		out.Streams[k] = StreamDecl{
			Direction:   s.Direction,
			Format:      s.Format,
			Description: s.Description,
			Affinity:    s.Affinity,
		}
	}
	return out
}

func cloneRaw(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(b))
	copy(out, b)
	return out
}
