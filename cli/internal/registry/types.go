package registry

// ProtocolVersion is the date-based wire protocol version this CLI speaks
// on publish and downstream registry calls.
const ProtocolVersion = "2026-05-01"

// PromotionInput holds validated promotion parameters for the publish call.
// TcAcceptedAt is set whenever BillingMode is "paid", regardless of listing
// (paid-any-listing T&C semantics — both public+paid and private+paid require T&C).
type PromotionInput struct {
	Listing                string  `json:"listing"`
	BillingMode            string  `json:"billingMode"`
	PricePerTask           *string `json:"pricePerTask,omitempty"`
	PricePerMinute         *string `json:"pricePerMinute,omitempty"`
	FreeTasksPerConsumer   *int    `json:"freeTasksPerConsumer,omitempty"`
	FreeMinutesPerConsumer *int    `json:"freeMinutesPerConsumer,omitempty"`
	TcAcceptedAt           string  `json:"tcAcceptedAt,omitempty"`
}

// PromotionFlags captures CLI flag values. Nil pointer = not provided (prompt interactively).
//
// NonInteractive is set by the caller when stdin is not a TTY (CI, scripted
// invocation). It forces fail-fast on any required value missing from flags
// rather than blocking on an EOF stdin read. AcceptTerms is the user-facing
// "I have accepted T&C" flag and is independent of NonInteractive.
type PromotionFlags struct {
	Listing        *string
	BillingMode    *string
	Price          *string
	PricePerTask   *string
	PricePerMinute *string
	FreeUnits      *int
	FreeTasks      *int
	FreeMinutes    *int
	AcceptTerms    bool
	NonInteractive bool
}

// Agent represents a registry agent record.
type Agent struct {
	AgentName   string `json:"agentName"`
	DisplayName string `json:"displayName"`
	Listing     string `json:"listing"`
	Card        *Card  `json:"card,omitempty"`
}

// Card holds the agent card metadata.
type Card struct {
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}

// Capabilities describes what the agent supports.
type Capabilities struct {
	TaskKinds []string `json:"taskKinds,omitempty"`
}

// IsStreaming returns true if the agent supports streaming (pipe) tasks.
func (a Agent) IsStreaming() bool {
	if a.Card == nil || a.Card.Capabilities == nil {
		return false
	}
	for _, k := range a.Card.Capabilities.TaskKinds {
		if k == "pipe" {
			return true
		}
	}
	return false
}

// IsRequest returns true if the agent supports request tasks.
func (a Agent) IsRequest() bool {
	if a.Card == nil || a.Card.Capabilities == nil {
		return false
	}
	for _, k := range a.Card.Capabilities.TaskKinds {
		if k == "request" {
			return true
		}
	}
	return false
}

// MaskAPIKey masks an API key for display (e.g., "bk_abc...xyz").
func MaskAPIKey(key string) string {
	if len(key) <= 9 {
		return "***"
	}
	return key[:6] + "..." + key[len(key)-3:]
}
