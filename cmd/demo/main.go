// Demo: scripted fake model, a policy that denies risky tools, and a
// supervisor that injects a note and blocks one call.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	. "github.com/sahuguet/agentharness/agent"
	"github.com/sahuguet/agentharness/supervise"
)

type fakeModel struct{} // asks for two tools, then ends the turn

func (fakeModel) Complete(_ context.Context, req ModelRequest) (ModelResponse, error) {
	last := req.Messages[len(req.Messages)-1].Blocks
	if last[0].Type == BlockToolResult {
		return ModelResponse{Message: Message{Role: RoleAssistant, Blocks: []Block{Text("", "Done: both calls were blocked.")}},
			Stop: StopEndTurn, Usage: Usage{InputTokens: 120, OutputTokens: 12}}, nil
	}
	use := func(id, name, in string) Block {
		return Block{Type: BlockToolUse, ToolCall: &ToolCall{ID: id, Name: name, Input: json.RawMessage(in)}}
	}
	return ModelResponse{Message: Message{Role: RoleAssistant, Blocks: []Block{use("c1", "clock", `{}`), use("c2", "rm", `{"path":"/"}`)}},
		Stop: StopToolUse, Usage: Usage{InputTokens: 80, OutputTokens: 30}}, nil
}

type denyRisky struct{}

func (denyRisky) CheckToolCall(_ context.Context, _ *Session, _ ToolCall, t ToolSpec) (Verdict, error) {
	if t.Risk >= RiskHigh {
		return Verdict{Action: Deny, Reason: "high-risk tool"}, nil
	}
	return Verdict{}, nil
}

type printer struct{}

func (printer) Publish(_ context.Context, ev Event) error {
	b, _ := json.Marshal(ev.Payload)
	if len(b) > 90 {
		b = append(b[:90], "…"...)
	}
	fmt.Printf("#%02d %-15s %s\n", ev.Seq, ev.Kind, b)
	return nil
}

func main() {
	c := context.Background()
	obj := json.RawMessage(`{"type":"object"}`)
	tools := new(LocalTools).
		Add(ToolSpec{Name: "clock", InputSchema: obj}, func(context.Context, *Session, ToolCall) (ToolResult, error) {
			return ToolResult{Content: time.Now().Format(time.RFC3339)}, nil
		}).
		Add(ToolSpec{Name: "rm", InputSchema: obj, Risk: RiskHigh}, func(context.Context, *Session, ToolCall) (ToolResult, error) {
			return ToolResult{Content: "deleted"}, nil
		})
	inbox := supervise.NewChanInbox()
	sup := supervise.New(inbox, nil)
	sup.Events = printer{}

	h, err := New(Services{
		Model: fakeModel{}, Tools: tools, Sessions: NewMemStore(),
		Context: StaticContext{System: "You are a helpful agent.", MaxTokens: 1024},
		Policy:  denyRisky{}, Supervisor: sup, Events: printer{},
	}, DefaultConfig)
	if err != nil {
		log.Fatal(err)
	}
	if err := h.StartSession(c, &Session{ID: "s1", UserID: "arnaud", Model: "claude-sonnet-5-5"}); err != nil {
		log.Fatal(err)
	}
	// The injection lands at the first safe point; the block applies when c1 is about to run.
	inbox.Post(supervise.Directive{SessionID: "s1", Kind: supervise.Inject, Content: []Block{Text("", "Never touch production paths.")}})
	inbox.Post(supervise.Directive{SessionID: "s1", Kind: supervise.Block, CallID: "c1", Reason: "clock is off-limits today"})

	s, err := h.Run(c, "s1", []Block{Text(FromUser, "What time is it? Also clean up /.")})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nmessages=%d usage=%+v\n", len(s.Messages), s.Usage)
}
