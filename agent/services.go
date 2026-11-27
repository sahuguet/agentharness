package agent

import (
	"context"
	"errors"
	"time"
)

type ctx = context.Context

var (
	ErrBudgetExceeded = errors.New("budget exceeded")
	ErrStopped        = errors.New("stopped by supervisor")
)

// ---- Hot path: called by the harness on every turn ----

type (
	ModelGateway interface { // providers, retries, fallback, caching
		Complete(ctx, ModelRequest) (ModelResponse, error)
	}
	ContextAssembler interface { // system prompt, skills, which tools to expose
		Build(ctx, *Session, []ToolSpec) (ModelRequest, error)
	}
	Compactor interface {
		MaybeCompact(ctx, *Session) (bool, error)
	}
	PolicyEngine interface { // stateless, per call
		CheckToolCall(ctx, *Session, ToolCall, ToolSpec) (Verdict, error)
	}
	Guardrail interface { // injection, PII, exfiltration
		CheckInput(ctx, *Session, []Block) (Verdict, error)
		CheckOutput(ctx, *Session, Message) (Verdict, error)
	}
	Approver interface { // human in the loop
		Approve(ctx, *Session, ToolCall, string) (Verdict, error)
	}
	BudgetGuard interface {
		Check(ctx, *Session) error // ErrBudgetExceeded stops the run
		Charge(ctx, *Session, Usage) error
		SetCap(ctx, string, BudgetCap) error
	}
)

// ---- Supervisor: the harness's only view of supervision ----
// See package supervise for an implementation with a gate + directive inbox.

type Supervisor interface {
	// SafePoint runs before each model call, when the transcript ends with a
	// user message. It may append to the session, block (pause), or return
	// ErrStopped.
	SafePoint(ctx, *Session) error
	// CheckTool runs before each tool call. Returning ErrStopped ends the run.
	CheckTool(ctx, *Session, ToolCall, ToolSpec, Event) (Verdict, error)
}

// ---- State & sinks ----

type (
	SessionStore interface { // persist, resume, fork, replay
		Create(ctx, *Session) error
		Load(ctx, string) (*Session, error)
		Save(ctx, *Session) error
		Fork(ctx, string, int) (*Session, error)
	}
	EventSink interface { // tracing, logs, evals, supervisor stream
		Publish(ctx, Event) error
	}
	MultiSink []EventSink
)

func (m MultiSink) Publish(c ctx, ev Event) error {
	var errs []error
	for _, s := range m {
		errs = append(errs, s.Publish(c, ev))
	}
	return errors.Join(errs...)
}

// ---- Tools: the model decides when. The harness only knows ToolRouter. ----

type (
	ToolRouter interface { // fronts MCP servers + a registry
		List(ctx, *Session) ([]ToolSpec, error)
		Call(ctx, *Session, ToolCall) (ToolResult, error)
	}
	ToolHandler func(ctx, *Session, ToolCall) (ToolResult, error)

	Sandbox interface {
		Exec(ctx, ExecRequest) (ExecResult, error)
	}
	ExecRequest struct {
		Image   string
		Command []string
		Files   map[string][]byte
		Env     map[string]string
		Timeout time.Duration
		Network bool
	}
	ExecResult struct {
		ExitCode       int
		Stdout, Stderr string
		Files          map[string][]byte
	}

	MemoryStore interface {
		Write(ctx, Memory) (string, error)
		Search(ctx, MemoryQuery) ([]Memory, error)
		Promote(ctx, string, MemoryScope) (string, error) // e.g. user -> team
		Forget(ctx, string) error
	}
	MemoryScope string
	Memory      struct {
		ID, Owner, Content string
		Scope              MemoryScope
		Tags, Lineage      []string // lineage: ids this derives from
		CreatedAt          time.Time
	}
	MemoryQuery struct {
		Scope       MemoryScope
		Owner, Text string
		Tags        []string
		Limit       int
	}

	Retriever interface {
		Search(ctx, string, int) ([]Document, error)
		Fetch(ctx, string) (Document, error)
	}
	Document struct {
		ID, Title, URI, Snippet string
		Score                   float64
	}

	SubAgentRunner interface { // runs another copy of this harness
		Spawn(ctx, SubAgentSpec) (string, error)
		Await(ctx, string) (SubAgentResult, error)
		Cancel(ctx, string) error
	}
	SubAgentSpec struct {
		Task, Model, ParentID string
		Tools                 []string
		Budget                BudgetCap
	}
	SubAgentResult struct {
		SessionID, Output, Err string
		Usage                  Usage
	}

	ArtifactStore interface {
		Put(ctx, string, string, []byte) (ArtifactRef, error) // session, name, data
		Get(ctx, string) ([]byte, ArtifactRef, error)
		List(ctx, string) ([]ArtifactRef, error)
	}
	ArtifactRef struct {
		ID, Name, MIME, URI string
		Size                int64
	}
)

// ---- Infrastructure around the loop ----

type (
	IdentityProvider interface {
		Resolve(ctx, string) (Principal, error)
	}
	Principal struct {
		UserID, OrgID string
		Scopes        []string
	}

	CredentialBroker interface { // short-lived scoped tokens; the harness holds no secrets
		Issue(ctx, Principal, string, []string) (Credential, error)
	}
	Credential struct {
		Token     string
		ExpiresAt time.Time
		Scopes    []string
	}

	Scheduler interface {
		Create(ctx, Trigger) (string, error)
		Delete(ctx, string) error
		List(ctx, string) ([]Trigger, error)
	}
	Trigger struct {
		ID, Kind, Schedule, Prompt, UserID string // Kind: cron | webhook | event
		Enabled                            bool
	}

	ChannelAdapter interface { // CLI, web, Slack, email
		Receive(ctx) (<-chan Inbound, error)
		Deliver(ctx, Event) error
	}
	Inbound struct {
		SessionID, UserID, Channel string
		Blocks                     []Block
	}

	SkillRegistry interface {
		Get(ctx, string, string) (Skill, error) // name, version
		Match(ctx, string, int) ([]Skill, error)
	}
	Skill struct {
		Name, Version, Description, Instructions string
		Tools                                    []string
	}

	Evaluator interface {
		Score(ctx, *Session) ([]EvalScore, error)
	}
	EvalScore struct {
		SessionID, Metric, Notes string
		Value                    float64
	}
)
