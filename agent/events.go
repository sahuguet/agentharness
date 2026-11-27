package agent

import "time"

// ---------------------------------------------------------------------------
// Events: harness -> world (observability, evals, supervisor stream)
// ---------------------------------------------------------------------------

type EventKind string

const (
	EvSessionStart  EventKind = "session_start"
	EvTurnStart     EventKind = "turn_start"
	EvModelRequest  EventKind = "model_request"
	EvModelResponse EventKind = "model_response"
	EvToolCall      EventKind = "tool_call"
	EvToolResult    EventKind = "tool_result"
	EvDirective     EventKind = "directive_applied"
	EvCompacted     EventKind = "compacted"
	EvTurnEnd       EventKind = "turn_end"
	EvSessionEnd    EventKind = "session_end"
	EvError         EventKind = "error"
)

type Event struct {
	Seq       uint64    `json:"seq"`
	SessionID string    `json:"session_id"`
	Kind      EventKind `json:"kind"`
	Time      time.Time `json:"time"`
	Payload   any       `json:"payload,omitempty"`
}

// ---------------------------------------------------------------------------
// Verdicts: answer from a blocking hook (policy, guardrail, supervisor gate)
// ---------------------------------------------------------------------------

type VerdictAction int

const (
	Allow VerdictAction = iota
	Rewrite
	Ask  // escalate to a human approver
	Deny // strongest: always wins when combining
)

type Verdict struct {
	Action  VerdictAction `json:"action"`
	Reason  string        `json:"reason,omitempty"`
	Rewrite *ToolCall     `json:"rewrite,omitempty"`
	By      string        `json:"by,omitempty"` // which service decided
}

// Combine picks the strongest verdict: Deny > Ask > Rewrite > Allow.
func Combine(vs ...Verdict) Verdict {
	out := Verdict{Action: Allow}
	for _, v := range vs {
		if v.Action > out.Action {
			out = v
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Directives: supervisor -> harness (async, applied at safe points)
// ---------------------------------------------------------------------------

type DirectiveKind string

const (
	DirInject      DirectiveKind = "inject"       // add content at next safe point
	DirBlock       DirectiveKind = "block"        // block a pending/future call
	DirRewrite     DirectiveKind = "rewrite"      // replace a call's args
	DirPause       DirectiveKind = "pause"        // hold before next model call
	DirResume      DirectiveKind = "resume"       //
	DirStop        DirectiveKind = "stop"         // end the session
	DirSetBudget   DirectiveKind = "set_budget"   //
	DirSwitchModel DirectiveKind = "switch_model" //
)

type Directive struct {
	ID        string        `json:"id"`
	SessionID string        `json:"session_id"`
	Kind      DirectiveKind `json:"kind"`
	// AfterSeq is the last event the supervisor had seen. The harness can
	// drop directives that lag too far behind (Config.MaxDirectiveLag).
	AfterSeq uint64     `json:"after_seq"`
	CallID   string     `json:"call_id,omitempty"`
	Content  []Block    `json:"content,omitempty"`
	Rewrite  *ToolCall  `json:"rewrite,omitempty"`
	Budget   *BudgetCap `json:"budget,omitempty"`
	Model    string     `json:"model,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Source   string     `json:"source,omitempty"` // which supervisor
}

type BudgetCap struct {
	MaxCostUSD   float64       `json:"max_cost_usd,omitempty"`
	MaxTokens    int           `json:"max_tokens,omitempty"`
	MaxWallClock time.Duration `json:"max_wall_clock,omitempty"`
}
