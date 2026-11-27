// Package supervise implements agent.Supervisor for a stateful shadow
// process that talks to the harness over two channels:
//
//   - Gate (sync): asked for a verdict before each tool call.
//   - Inbox (async): directives posted any time, applied by the harness only
//     at safe points (before a model call) or before a tool call.
package supervise

import (
	"context"
	"fmt"
	"sync"

	"github.com/sahuguet/agentharness/agent"
)

type Kind string

const (
	Inject      Kind = "inject" // added at the next safe point, tagged FromSupervisor
	Block       Kind = "block"  // deny a call by ID
	Rewrite     Kind = "rewrite"
	Pause       Kind = "pause" // hold before the next model call
	Resume      Kind = "resume"
	Stop        Kind = "stop" // end the run with agent.ErrStopped
	SetBudget   Kind = "set_budget"
	SwitchModel Kind = "switch_model"
)

const EvDirective agent.EventKind = "directive"

type Directive struct {
	ID, SessionID string
	Kind          Kind
	AfterSeq      uint64 // last event the sender saw; see Supervisor.MaxLag
	CallID        string
	Content       []agent.Block
	Replacement   *agent.ToolCall
	Budget        *agent.BudgetCap
	Model, Reason string
	Source        string // which supervisor sent it
}

type (
	Gate interface {
		Gate(context.Context, agent.Event) (agent.Verdict, error)
	}
	Inbox interface {
		Drain(ctx context.Context, sessionID string) ([]Directive, error)
		Wait(ctx context.Context, sessionID string) error // until a directive arrives
	}
)

// Supervisor adapts a Gate and an Inbox to agent.Supervisor. It keeps the
// per-session state (blocked calls, rewrites, pending injections, pause).
type Supervisor struct {
	Inbox  Inbox             // required
	Gate   Gate              // optional
	Budget agent.BudgetGuard // optional, for SetBudget
	Events agent.EventSink   // optional, records applied directives
	MaxLag uint64            // drop directives this many events stale; 0 = never

	mu       sync.Mutex
	sessions map[string]*state
}

type state struct {
	blocked  map[string]string
	rewrites map[string]*agent.ToolCall
	inject   []agent.Block
	paused   bool
}

func New(inbox Inbox, gate Gate) *Supervisor { return &Supervisor{Inbox: inbox, Gate: gate} }

// SafePoint applies directives, waits while paused, then flushes injections.
func (sv *Supervisor) SafePoint(c context.Context, s *agent.Session) error {
	for {
		st, err := sv.drain(c, s)
		if err != nil {
			return err
		}
		if !st.paused {
			if len(st.inject) > 0 {
				s.AppendUser(st.inject...)
				st.inject = nil
			}
			return nil
		}
		if err := sv.Inbox.Wait(c, s.ID); err != nil {
			return err
		}
	}
}

// CheckTool applies directives (block/rewrite/stop can land between calls),
// then asks the Gate.
func (sv *Supervisor) CheckTool(c context.Context, s *agent.Session, call agent.ToolCall, _ agent.ToolSpec, ev agent.Event) (agent.Verdict, error) {
	st, err := sv.drain(c, s)
	switch {
	case err != nil:
		return agent.Verdict{}, err
	case st.blocked[call.ID] != "":
		return agent.Verdict{Action: agent.Deny, Reason: st.blocked[call.ID]}, nil
	case sv.Gate != nil:
		v, err := sv.Gate.Gate(c, ev)
		if err != nil || v.Action != agent.Allow {
			return v, err
		}
	}
	if rw := st.rewrites[call.ID]; rw != nil {
		return agent.Verdict{Action: agent.Rewrite, Replacement: rw, Reason: "rewritten by supervisor"}, nil
	}
	return agent.Verdict{}, nil
}

// Forget drops a session's state once it is finished.
func (sv *Supervisor) Forget(sessionID string) {
	sv.mu.Lock()
	delete(sv.sessions, sessionID)
	sv.mu.Unlock()
}

func (sv *Supervisor) drain(c context.Context, s *agent.Session) (*state, error) {
	sv.mu.Lock()
	if sv.sessions == nil {
		sv.sessions = map[string]*state{}
	}
	st := sv.sessions[s.ID]
	if st == nil {
		st = &state{blocked: map[string]string{}, rewrites: map[string]*agent.ToolCall{}}
		sv.sessions[s.ID] = st
	}
	sv.mu.Unlock()

	ds, err := sv.Inbox.Drain(c, s.ID)
	if err != nil {
		agent.Emit(c, sv.Events, s, agent.EvError, "inbox: "+err.Error())
		return st, nil // a broken inbox shouldn't kill the run
	}
	for _, d := range ds {
		if sv.MaxLag > 0 && d.AfterSeq+sv.MaxLag < s.Seq {
			continue // stale: the run has moved on
		}
		agent.Emit(c, sv.Events, s, EvDirective, d)
		switch d.Kind {
		case Inject:
			for _, b := range d.Content {
				b.Source = agent.FromSupervisor
				st.inject = append(st.inject, b)
			}
		case Block:
			st.blocked[d.CallID] = cmp(d.Reason, "blocked")
		case Rewrite:
			st.rewrites[d.CallID] = d.Replacement
		case Pause, Resume:
			st.paused = d.Kind == Pause
		case Stop:
			return st, fmt.Errorf("%w: %s", agent.ErrStopped, d.Reason)
		case SetBudget:
			if sv.Budget != nil && d.Budget != nil {
				_ = sv.Budget.SetCap(c, s.ID, *d.Budget)
			}
		case SwitchModel:
			s.Model = d.Model
		}
	}
	return st, nil
}

func cmp(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ChanInbox is an in-process Inbox; a supervisor goroutine calls Post.
type ChanInbox struct {
	mu      sync.Mutex
	queue   map[string][]Directive
	waiters map[string]chan struct{}
}

func NewChanInbox() *ChanInbox {
	return &ChanInbox{queue: map[string][]Directive{}, waiters: map[string]chan struct{}{}}
}

func (b *ChanInbox) Post(d Directive) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queue[d.SessionID] = append(b.queue[d.SessionID], d)
	if w := b.waiters[d.SessionID]; w != nil {
		close(w)
		delete(b.waiters, d.SessionID)
	}
}

func (b *ChanInbox) Drain(_ context.Context, id string) ([]Directive, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ds := b.queue[id]
	delete(b.queue, id)
	return ds, nil
}

func (b *ChanInbox) Wait(c context.Context, id string) error {
	b.mu.Lock()
	if len(b.queue[id]) > 0 {
		b.mu.Unlock()
		return nil
	}
	if b.waiters[id] == nil {
		b.waiters[id] = make(chan struct{})
	}
	w := b.waiters[id]
	b.mu.Unlock()
	select {
	case <-w:
		return nil
	case <-c.Done():
		return c.Err()
	}
}
