package agent

import (
	"context"
	"time"
)

// The model reaches everything in this file through tool calls. The harness
// only knows ToolRouter; the typed interfaces below are what concrete tool
// servers (MCP or otherwise) implement, and are adapted into tools.

// ToolRouter lists and runs tools (typically fronting MCP servers + a registry).
type ToolRouter interface {
	List(ctx context.Context, s *Session) ([]ToolSpec, error)
	Call(ctx context.Context, s *Session, call ToolCall) (ToolResult, error)
}

// ToolHandler is one tool implemented in-process.
type ToolHandler func(ctx context.Context, s *Session, call ToolCall) (ToolResult, error)

// ---------------------------------------------------------------------------
// Sandbox / code execution
// ---------------------------------------------------------------------------

type ExecRequest struct {
	Image   string // container / microVM image
	Command []string
	Files   map[string][]byte // files to place in the workdir
	Env     map[string]string
	Timeout time.Duration
	Network bool
}

type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Files    map[string][]byte // files produced
	Duration time.Duration
}

type Sandbox interface {
	Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
}

// ---------------------------------------------------------------------------
// Memory
// ---------------------------------------------------------------------------

type MemoryScope string

const (
	ScopeUser    MemoryScope = "user"
	ScopeTeam    MemoryScope = "team"
	ScopeSession MemoryScope = "session"
)

type Memory struct {
	ID        string
	Scope     MemoryScope
	Owner     string
	Content   string
	Tags      []string
	Lineage   []string // ids of memories / sessions this derives from
	CreatedAt time.Time
}

type MemoryQuery struct {
	Scope MemoryScope
	Owner string
	Text  string
	Tags  []string
	Limit int
}

type MemoryStore interface {
	Write(ctx context.Context, m Memory) (string, error)
	Search(ctx context.Context, q MemoryQuery) ([]Memory, error)
	Promote(ctx context.Context, id string, to MemoryScope) (string, error)
	Forget(ctx context.Context, id string) error
}

// ---------------------------------------------------------------------------
// Retrieval / search
// ---------------------------------------------------------------------------

type Document struct {
	ID, Title, URI, Snippet string
	Score                   float64
}

type Retriever interface {
	Search(ctx context.Context, query string, k int) ([]Document, error)
	Fetch(ctx context.Context, uri string) (Document, error)
}

// ---------------------------------------------------------------------------
// Sub-agents
// ---------------------------------------------------------------------------

type SubAgentSpec struct {
	Task     string
	Model    string
	Tools    []string // allowed tool names
	Budget   BudgetCap
	ParentID string
	Blocking bool // wait for the result vs run in background
}

type SubAgentResult struct {
	SessionID string
	Output    string
	Usage     Usage
	Err       string
}

// SubAgentRunner starts another instance of this same harness.
type SubAgentRunner interface {
	Spawn(ctx context.Context, spec SubAgentSpec) (sessionID string, err error)
	Await(ctx context.Context, sessionID string) (SubAgentResult, error)
	Cancel(ctx context.Context, sessionID string) error
}

// ---------------------------------------------------------------------------
// Files & artifacts
// ---------------------------------------------------------------------------

type ArtifactRef struct {
	ID, Name, MIME, URI string
	Size                int64
}

type ArtifactStore interface {
	Put(ctx context.Context, sessionID, name, mime string, data []byte) (ArtifactRef, error)
	Get(ctx context.Context, id string) ([]byte, ArtifactRef, error)
	List(ctx context.Context, sessionID string) ([]ArtifactRef, error)
}
