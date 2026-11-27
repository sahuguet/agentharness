package agent

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Nop is the default for every optional service: allows tool calls, denies
// approvals (nobody to ask), never compacts, no budget, no supervision,
// discards events.
type Nop struct{}

func (Nop) CheckToolCall(ctx, *Session, ToolCall, ToolSpec) (Verdict, error) { return Verdict{}, nil }
func (Nop) SafePoint(ctx, *Session) error                                    { return nil }
func (Nop) CheckTool(ctx, *Session, ToolCall, ToolSpec, Event) (Verdict, error) {
	return Verdict{}, nil
}
func (Nop) Approve(ctx, *Session, ToolCall, string) (Verdict, error) {
	return Verdict{Action: Deny, Reason: "no approver configured"}, nil
}
func (Nop) MaybeCompact(ctx, *Session) (bool, error) { return false, nil }
func (Nop) Check(ctx, *Session) error                { return nil }
func (Nop) Charge(ctx, *Session, Usage) error        { return nil }
func (Nop) SetCap(ctx, string, BudgetCap) error      { return nil }
func (Nop) Publish(ctx, Event) error                 { return nil }

// MemStore is an in-memory SessionStore (deep copies via JSON).
type MemStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewMemStore() *MemStore { return &MemStore{m: map[string][]byte{}} }

func (st *MemStore) Create(c ctx, s *Session) error {
	if _, err := st.Load(c, s.ID); err == nil {
		return fmt.Errorf("session %s exists", s.ID)
	}
	return st.Save(c, s)
}

func (st *MemStore) Save(_ ctx, s *Session) error {
	b, err := json.Marshal(s)
	st.mu.Lock()
	st.m[s.ID] = b
	st.mu.Unlock()
	return err
}

func (st *MemStore) Load(_ ctx, id string) (*Session, error) {
	st.mu.Lock()
	b, ok := st.m[id]
	st.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("session %s not found", id)
	}
	var s Session
	return &s, json.Unmarshal(b, &s)
}

func (st *MemStore) Fork(c ctx, id string, at int) (*Session, error) {
	s, err := st.Load(c, id)
	if err != nil {
		return nil, err
	}
	if at >= 0 && at < len(s.Messages) {
		s.Messages = s.Messages[:at]
	}
	s.ID = fmt.Sprintf("%s-fork-%d", id, s.Seq)
	return s, st.Save(c, s)
}

// StaticContext is a fixed system prompt exposing every tool.
type StaticContext struct {
	System    string
	MaxTokens int
}

func (a StaticContext) Build(_ ctx, s *Session, tools []ToolSpec) (ModelRequest, error) {
	return ModelRequest{s.Model, a.System, s.Messages, tools, a.MaxTokens}, nil
}

// LocalTools is an in-process ToolRouter; swap for an MCP client router.
type LocalTools struct {
	specs    []ToolSpec
	handlers map[string]ToolHandler
}

func (l *LocalTools) Add(spec ToolSpec, h ToolHandler) *LocalTools {
	if l.handlers == nil {
		l.handlers = map[string]ToolHandler{}
	}
	l.specs, l.handlers[spec.Name] = append(l.specs, spec), h
	return l
}

func (l *LocalTools) List(ctx, *Session) ([]ToolSpec, error) { return l.specs, nil }

func (l *LocalTools) Call(c ctx, s *Session, call ToolCall) (ToolResult, error) {
	if h := l.handlers[call.Name]; h != nil {
		return h(c, s, call)
	}
	return ToolResult{}, fmt.Errorf("unknown tool %q", call.Name)
}
