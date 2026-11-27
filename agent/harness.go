package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Services wires the harness to the world. Model, Tools, Context and Sessions
// are required; New fills the rest with Nop.
type Services struct {
	Model      ModelGateway
	Tools      ToolRouter
	Context    ContextAssembler
	Sessions   SessionStore
	Compactor  Compactor
	Policy     PolicyEngine
	Guardrails []Guardrail
	Approver   Approver
	Budget     BudgetGuard
	Supervisor Supervisor
	Events     EventSink
}

type Config struct {
	MaxTurns                 int
	ToolTimeout, GateTimeout time.Duration
	FailClosedAt             RiskLevel // on policy/supervisor failure, deny tools at or above this risk
}

var DefaultConfig = Config{MaxTurns: 50, ToolTimeout: 2 * time.Minute, GateTimeout: 2 * time.Second, FailClosedAt: RiskHigh}

type Harness struct {
	Services
	Config
}

func New(s Services, c Config) (*Harness, error) {
	if s.Model == nil || s.Tools == nil || s.Context == nil || s.Sessions == nil {
		return nil, errors.New("agent: Model, Tools, Context and Sessions are required")
	}
	def(&s.Compactor, Compactor(Nop{}))
	def(&s.Policy, PolicyEngine(Nop{}))
	def(&s.Approver, Approver(Nop{})) // denies: nobody to ask => fail closed
	def(&s.Budget, BudgetGuard(Nop{}))
	def(&s.Supervisor, Supervisor(Nop{}))
	def(&s.Events, EventSink(Nop{}))
	if c.MaxTurns == 0 {
		c = DefaultConfig
	}
	return &Harness{s, c}, nil
}

func def[T comparable](p *T, v T) {
	var zero T
	if *p == zero {
		*p = v
	}
}

func (h *Harness) StartSession(c context.Context, s *Session) error {
	if err := h.Sessions.Create(c, s); err != nil {
		return err
	}
	Emit(c, h.Events, s, EvSessionStart, nil)
	return h.Sessions.Save(c, s)
}

// Run processes one user input until the model ends its turn.
func (h *Harness) Run(c context.Context, sessionID string, input []Block) (_ *Session, err error) {
	s, err := h.Sessions.Load(c, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			Emit(c, h.Events, s, EvError, err.Error())
		}
		err = errors.Join(err, h.Sessions.Save(context.WithoutCancel(c), s))
	}()

	if v := h.guard(func(g Guardrail) (Verdict, error) { return g.CheckInput(c, s, input) }); v.Action == Deny {
		return s, fmt.Errorf("input blocked by %s: %s", v.By, v.Reason)
	}
	s.AppendUser(input...)
	Emit(c, h.Events, s, EvTurnStart, input)

	for turn := 0; turn < h.MaxTurns; turn++ {
		resp, specs, err := h.callModel(c, s)
		if err != nil {
			return s, err
		}
		calls := resp.Message.ToolCalls()
		if len(calls) == 0 {
			Emit(c, h.Events, s, EvTurnEnd, resp.Stop)
			return s, nil
		}
		results := make([]Block, 0, len(calls))
		for _, call := range calls {
			res, err := h.runTool(c, s, specs, call)
			if err != nil {
				return s, err
			}
			results = append(results, Block{Type: BlockToolResult, ToolResult: &res, Source: FromTool})
		}
		s.Messages = append(s.Messages, Message{RoleUser, results})
		if err := h.Sessions.Save(c, s); err != nil {
			return s, err
		}
	}
	return s, fmt.Errorf("max turns (%d) reached", h.MaxTurns)
}

// callModel: safe point, budget, compaction, context, model call, output guardrails.
func (h *Harness) callModel(c context.Context, s *Session) (resp ModelResponse, specs map[string]ToolSpec, err error) {
	if err = h.Supervisor.SafePoint(c, s); err != nil {
		return
	}
	if err = h.Budget.Check(c, s); err != nil {
		return
	}
	if ok, err := h.Compactor.MaybeCompact(c, s); err != nil {
		return resp, nil, fmt.Errorf("compact: %w", err)
	} else if ok {
		Emit(c, h.Events, s, EvCompacted, len(s.Messages))
	}
	list, err := h.Tools.List(c, s)
	if err != nil {
		return resp, nil, fmt.Errorf("list tools: %w", err)
	}
	specs = make(map[string]ToolSpec, len(list))
	for _, sp := range list {
		specs[sp.Name] = sp
	}
	req, err := h.Context.Build(c, s, list)
	if err != nil {
		return resp, nil, fmt.Errorf("build context: %w", err)
	}
	Emit(c, h.Events, s, EvModelRequest, req)
	if resp, err = h.Model.Complete(c, req); err != nil {
		return resp, nil, fmt.Errorf("model: %w", err)
	}
	s.Usage.Add(resp.Usage)
	if err = h.Budget.Charge(c, s, resp.Usage); err != nil {
		return
	}
	for i := range resp.Message.Blocks {
		resp.Message.Blocks[i].Source = FromModel
	}
	if v := h.guard(func(g Guardrail) (Verdict, error) { return g.CheckOutput(c, s, resp.Message) }); v.Action == Deny {
		return resp, nil, fmt.Errorf("output blocked by %s: %s", v.By, v.Reason)
	}
	s.Messages = append(s.Messages, resp.Message)
	Emit(c, h.Events, s, EvModelResponse, resp)
	return resp, specs, nil
}

// runTool: policy + supervisor -> approval -> execute. Only ErrStopped is
// returned as an error; every other outcome becomes the tool result.
func (h *Harness) runTool(c context.Context, s *Session, specs map[string]ToolSpec, call ToolCall) (res ToolResult, err error) {
	defer func() {
		if err == nil {
			res.CallID = call.ID
			Emit(c, h.Events, s, EvToolResult, res)
		}
	}()
	deny := func(by, why string) (ToolResult, error) {
		return ToolResult{Content: fmt.Sprintf("tool call blocked by %s: %s", by, why), IsError: true}, nil
	}
	spec, ok := specs[call.Name]
	if !ok {
		return deny("harness", "unknown tool "+call.Name)
	}
	ev := Emit(c, h.Events, s, EvToolCall, call)

	pv, perr := h.Policy.CheckToolCall(c, s, call, spec)
	if perr != nil {
		pv = h.failVerdict(spec, perr)
	}
	gc, cancel := context.WithTimeout(c, h.GateTimeout)
	sv, serr := h.Supervisor.CheckTool(gc, s, call, spec, ev)
	cancel()
	if errors.Is(serr, ErrStopped) {
		return res, serr
	} else if serr != nil {
		sv = h.failVerdict(spec, serr)
	}
	pv.By, sv.By = or(pv.By, "policy"), or(sv.By, "supervisor")

	v := Combine(pv, sv)
	if v.Action == Ask {
		av, aerr := h.Approver.Approve(c, s, call, v.Reason)
		if aerr != nil {
			av = Verdict{Action: Deny, Reason: aerr.Error()}
		} else if av.Action == Ask { // approver can't defer further
			av.Action = Deny
		}
		v, v.By = av, "approver"
	}
	switch v.Action {
	case Deny:
		return deny(v.By, v.Reason)
	case Rewrite:
		call = Replace(call, v.Replacement)
	}
	tc, cancel := context.WithTimeout(c, h.ToolTimeout)
	defer cancel()
	if res, err = h.Tools.Call(tc, s, call); err != nil {
		res, err = ToolResult{Content: err.Error(), IsError: true}, nil
	}
	return res, nil
}

func (h *Harness) failVerdict(spec ToolSpec, err error) Verdict {
	if spec.Risk >= h.FailClosedAt {
		return Verdict{Action: Deny, Reason: "unavailable, fail closed: " + err.Error()}
	}
	return Verdict{Action: Allow, Reason: "unavailable, fail open: " + err.Error()}
}

func (h *Harness) guard(check func(Guardrail) (Verdict, error)) Verdict {
	var vs []Verdict
	for _, g := range h.Guardrails {
		v, err := check(g)
		if err != nil {
			v = Verdict{Action: Deny, Reason: err.Error()}
		}
		v.By = or(v.By, "guardrail")
		vs = append(vs, v)
	}
	return Combine(vs...)
}

// Emit numbers and publishes an event; telemetry failures never block the
// loop. Services that act on a session (e.g. a supervisor) use it too.
func Emit(c context.Context, sink EventSink, s *Session, kind EventKind, payload any) Event {
	s.Seq++
	ev := Event{s.Seq, s.ID, kind, time.Now(), payload}
	if sink != nil {
		_ = sink.Publish(c, ev)
	}
	return ev
}

// Replace swaps in a replacement call, keeping the original call ID.
func Replace(call ToolCall, with *ToolCall) ToolCall {
	if with == nil {
		return call
	}
	w := *with
	w.ID = call.ID
	return w
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
