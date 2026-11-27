// Package agent is a minimal, modular agent harness: the harness owns the
// loop, every other capability is a service behind an interface.
package agent

import (
	"encoding/json"
	"time"
)

// ---- Transcript ----

type (
	Role       string
	BlockType  string
	Provenance string // who produced a block
	StopReason string
	RiskLevel  int
)

const (
	RoleUser, RoleAssistant Role = "user", "assistant"

	BlockText, BlockToolUse, BlockToolResult BlockType = "text", "tool_use", "tool_result"

	FromUser, FromModel, FromTool, FromSupervisor Provenance = "user", "model", "tool", "supervisor"

	StopEndTurn, StopToolUse, StopMaxTokens StopReason = "end_turn", "tool_use", "max_tokens"
)

const (
	RiskLow RiskLevel = iota
	RiskMedium
	RiskHigh
)

type Block struct {
	Type       BlockType
	Text       string      `json:",omitempty"`
	ToolCall   *ToolCall   `json:",omitempty"`
	ToolResult *ToolResult `json:",omitempty"`
	Source     Provenance  `json:",omitempty"`
}

type Message struct {
	Role   Role
	Blocks []Block
}

type ToolSpec struct {
	Name, Description string
	InputSchema       json.RawMessage
	Risk              RiskLevel // picks fail-open vs fail-closed
}

type ToolCall struct {
	ID, Name string
	Input    json.RawMessage
}

type ToolResult struct {
	CallID, Content string
	IsError         bool `json:",omitempty"`
}

type Usage struct {
	InputTokens, OutputTokens int
	CostUSD                   float64
}

type ModelRequest struct {
	Model, System string
	Messages      []Message
	Tools         []ToolSpec
	MaxTokens     int
}

type ModelResponse struct {
	Message Message
	Stop    StopReason
	Usage   Usage
}

// Session is all the state of a run; the harness itself is stateless.
type Session struct {
	ID, UserID, Model string
	Messages          []Message
	Seq               uint64 // last event number
	Usage             Usage
	Meta              map[string]string `json:",omitempty"`
}

func Text(src Provenance, s string) Block { return Block{Type: BlockText, Text: s, Source: src} }

func (u *Usage) Add(o Usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CostUSD += o.CostUSD
}

func (m Message) ToolCalls() (out []ToolCall) {
	for _, b := range m.Blocks {
		if b.Type == BlockToolUse && b.ToolCall != nil {
			out = append(out, *b.ToolCall)
		}
	}
	return out
}

// AppendUser adds blocks to a trailing user message (e.g. next to tool
// results) or starts one. This keeps supervisor injections API-valid.
func (s *Session) AppendUser(bs ...Block) {
	if n := len(s.Messages); n > 0 && s.Messages[n-1].Role == RoleUser {
		s.Messages[n-1].Blocks = append(s.Messages[n-1].Blocks, bs...)
		return
	}
	s.Messages = append(s.Messages, Message{RoleUser, bs})
}

// ---- Events (harness -> world) and verdicts (blocking hooks) ----

type EventKind string

const (
	EvSessionStart  EventKind = "session_start"
	EvTurnStart     EventKind = "turn_start"
	EvModelRequest  EventKind = "model_request"
	EvModelResponse EventKind = "model_response"
	EvToolCall      EventKind = "tool_call"
	EvToolResult    EventKind = "tool_result"
	EvCompacted     EventKind = "compacted"
	EvTurnEnd       EventKind = "turn_end"
	EvError         EventKind = "error"
)

type Event struct {
	Seq       uint64
	SessionID string
	Kind      EventKind
	Time      time.Time
	Payload   any `json:",omitempty"`
}

type Action int

const (
	Allow Action = iota
	Rewrite
	Ask  // escalate to a human
	Deny // strongest
)

type Verdict struct {
	Action      Action
	Reason, By  string    `json:",omitempty"`
	Replacement *ToolCall `json:",omitempty"` // for Rewrite
}

// Combine returns the strongest verdict: Deny > Ask > Rewrite > Allow.
func Combine(vs ...Verdict) (out Verdict) {
	for _, v := range vs {
		if v.Action > out.Action {
			out = v
		}
	}
	return out
}

type BudgetCap struct {
	MaxCostUSD   float64
	MaxTokens    int
	MaxWallClock time.Duration
}
