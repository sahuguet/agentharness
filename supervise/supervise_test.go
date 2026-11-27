package supervise

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sahuguet/agentharness/agent"
)

func TestPauseResumeStop(t *testing.T) {
	c := context.Background()
	in := NewChanInbox()
	sv := New(in, nil)
	s := &agent.Session{ID: "s"}

	// Pause blocks SafePoint until Resume arrives; the injection is flushed after.
	in.Post(Directive{SessionID: "s", Kind: Pause})
	in.Post(Directive{SessionID: "s", Kind: Inject, Content: []agent.Block{agent.Text("", "hi")}})
	go func() { time.Sleep(50 * time.Millisecond); in.Post(Directive{SessionID: "s", Kind: Resume}) }()
	start := time.Now()
	if err := sv.SafePoint(c, s); err != nil || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("pause not honored: err=%v elapsed=%v", err, time.Since(start))
	}
	if got := s.Messages[0].Blocks[0]; got.Text != "hi" || got.Source != agent.FromSupervisor {
		t.Fatalf("injection = %+v", got)
	}

	// Rewrite, then Stop.
	in.Post(Directive{SessionID: "s", Kind: Rewrite, CallID: "c1", Replacement: &agent.ToolCall{Name: "safe"}})
	if v, _ := sv.CheckTool(c, s, agent.ToolCall{ID: "c1"}, agent.ToolSpec{}, agent.Event{}); v.Action != agent.Rewrite {
		t.Fatalf("want rewrite, got %+v", v)
	}
	in.Post(Directive{SessionID: "s", Kind: Stop, Reason: "enough"})
	if _, err := sv.CheckTool(c, s, agent.ToolCall{ID: "c2"}, agent.ToolSpec{}, agent.Event{}); !errors.Is(err, agent.ErrStopped) {
		t.Fatalf("want ErrStopped, got %v", err)
	}
}
