package agent

import (
	"context"
	"time"
)

// Infrastructure services that live around the harness, not in its loop.

// ---------------------------------------------------------------------------
// Credentials & identity
// ---------------------------------------------------------------------------

type Principal struct {
	UserID string
	OrgID  string
	Scopes []string
}

type IdentityProvider interface {
	Resolve(ctx context.Context, sessionID string) (Principal, error)
}

type Credential struct {
	Token     string
	ExpiresAt time.Time
	Scopes    []string
}

// CredentialBroker hands tools short-lived, narrowly scoped tokens.
// The harness never holds secrets.
type CredentialBroker interface {
	Issue(ctx context.Context, p Principal, resource string, scopes []string) (Credential, error)
}

// ---------------------------------------------------------------------------
// Triggers & channels
// ---------------------------------------------------------------------------

type TriggerKind string

const (
	TriggerCron    TriggerKind = "cron"
	TriggerWebhook TriggerKind = "webhook"
	TriggerEvent   TriggerKind = "event"
)

type Trigger struct {
	ID       string
	Kind     TriggerKind
	Schedule string // cron expression, for TriggerCron
	Prompt   string
	UserID   string
	Enabled  bool
}

type Scheduler interface {
	Create(ctx context.Context, t Trigger) (string, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, userID string) ([]Trigger, error)
}

type InboundMessage struct {
	SessionID string // empty = new session
	UserID    string
	Blocks    []Block
	Channel   string
}

// ChannelAdapter is a thin client: CLI, web UI, Slack, email.
type ChannelAdapter interface {
	Receive(ctx context.Context) (<-chan InboundMessage, error)
	// Deliver streams events (or just the final message) back to the user.
	Deliver(ctx context.Context, ev Event) error
}

// ---------------------------------------------------------------------------
// Skills & evals
// ---------------------------------------------------------------------------

type Skill struct {
	Name, Version, Description string
	Instructions               string
	Tools                      []string
}

type SkillRegistry interface {
	Get(ctx context.Context, name, version string) (Skill, error)
	Match(ctx context.Context, query string, k int) ([]Skill, error)
}

type EvalScore struct {
	SessionID string
	Metric    string
	Value     float64
	Notes     string
}

type Evaluator interface {
	Score(ctx context.Context, s *Session) ([]EvalScore, error)
}
