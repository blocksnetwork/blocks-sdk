// Package agentsearch is the one registry search behind the init pickers and 'blocks search'.
package agentsearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

const agentsPath = "/api/v1/registry/agents"

// MaxLimit is the backend's page ceiling for the list endpoint.
const MaxLimit = 100

const (
	ListingPublic  = "public"
	ListingPrivate = "private"
)

// Status values are the registry's agent-list status filter.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusAll     = "all"
)

var Statuses = []string{StatusOnline, StatusOffline, StatusAll}

type Agent struct {
	AgentName   string `json:"agentName"`
	DisplayName string `json:"displayName"`
	Summary     string `json:"summary,omitempty"`
	// Listing is "public" or "private"; a private hit is one the caller owns or was granted.
	Listing string `json:"visibility"`
	OrgName string `json:"orgName,omitempty"`
	// Online is nil when the registry did not report an online count.
	Online *bool `json:"online"`
}

func (a Agent) Availability() string {
	switch {
	case a.Online == nil:
		return ""
	case *a.Online:
		return "online"
	default:
		return "offline"
	}
}

// Query asks for one page; an empty Text browses alphabetically, and Cursor is "" or a prior Result.Next.
type Query struct {
	Text   string
	Limit  int
	Cursor string
	// Status is one of Statuses, or "" for the registry's default.
	Status string
}

type Result struct {
	Agents        []Agent
	Authenticated bool
	Status        string
	// ParseError means the registry could not parse the query's operators and matched it as plain text.
	ParseError bool
	// Next is the following page's cursor, or ""; a full last page gets one too, so that page may be empty.
	Next string
}

type ErrorKind int

const (
	// KindAuth is HTTP 401/403; any other HTTP failure is KindServer.
	KindAuth ErrorKind = iota + 1
	KindConnectivity
	KindServer
)

type Error struct {
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func KindOf(err error) ErrorKind {
	var se *Error
	if errors.As(err, &se) {
		return se.Kind
	}
	return 0
}

type listResponse struct {
	Agents []struct {
		AgentName   string `json:"agentName"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		CardSummary string `json:"cardSummary"`
		Listing     string `json:"listing"`
		OrgName     string `json:"orgName"`
		OnlineCount *int   `json:"onlineCount"`
	} `json:"agents"`
	Next             *string `json:"next"`
	SearchParseError bool    `json:"searchParseError"`
}

// Search returns one page, best first.
func Search(ctx context.Context, client *blocksapi.Client, q Query) (Result, error) {
	authenticated := client.APIKey != ""
	resp, err := client.Get(ctx, agentsPath, buildQuery(q, authenticated))
	if err != nil {
		return Result{}, classify(err)
	}
	defer resp.Body.Close()

	var parsed listResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return Result{}, &Error{Kind: KindServer, Err: fmt.Errorf("unreadable registry response: %w", err)}
	}

	out := Result{
		Agents:        make([]Agent, 0, len(parsed.Agents)),
		Authenticated: authenticated,
		Status:        q.Status,
		ParseError:    parsed.SearchParseError,
	}
	if parsed.Next != nil {
		out.Next = *parsed.Next
	}
	for _, row := range parsed.Agents {
		a := Agent{
			AgentName:   row.AgentName,
			DisplayName: row.DisplayName,
			Summary:     firstNonEmpty(row.CardSummary, row.Description),
			Listing:     row.Listing,
			OrgName:     row.OrgName,
		}
		if row.OnlineCount != nil {
			online := *row.OnlineCount > 0
			a.Online = &online
		}
		out.Agents = append(out.Agents, a)
	}
	return out, nil
}

func buildQuery(q Query, authenticated bool) url.Values {
	v := url.Values{
		"limit":         {strconv.Itoa(q.Limit)},
		"includeTotals": {"false"},
	}
	if text := strings.TrimSpace(q.Text); text != "" {
		v.Set("q", text)
		v.Set("sort", "relevance")
	} else {
		v.Set("sort", "name")
	}
	if authenticated {
		v.Set("listing", ListingPublic+","+ListingPrivate)
	}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	return v
}

func classify(err error) error {
	var apiErr *blocksapi.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden {
			return &Error{Kind: KindAuth, Err: err}
		}
		return &Error{Kind: KindServer, Err: err}
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &Error{Kind: KindConnectivity, Err: err}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
