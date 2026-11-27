# agentharness

A minimal agent harness in Go. The harness owns only the loop. Everything else is a service behind an interface.

```
agent/
  types.go     transcript, tools, model I/O, Session, Event, Verdict
  services.go  every service interface: hot path, Supervisor hook, state, tools, infra
  harness.go   the loop
  defaults.go  Nop (all optional services), MemStore, StaticContext, LocalTools
supervise/     agent.Supervisor built on a sync Gate + async directive Inbox
cmd/demo/      fake model + policy + supervisor injection and block
```

Run the demo with `go run ./cmd/demo`, and the tests with `go test ./...`.

## Loop

1. `Supervisor.SafePoint`: the supervisor may inject content, pause or stop the run.
2. Check the budget, then compact if needed.
3. List tools, build the context, call the model, charge usage, run output guardrails.
4. For each tool call:
   - run `Policy.CheckToolCall` and `Supervisor.CheckTool`, combined so Deny > Ask > Rewrite > Allow
   - ask the approver if a verdict says Ask
   - execute with a timeout
5. Append the results, save, and repeat until the model ends its turn.

The harness knows nothing about directives. `supervise.Supervisor` turns posted directives (inject, block, rewrite, pause, resume, stop, set_budget, switch_model) into those two hooks and keeps the per-session state.

## Rules

- Injected content is tagged `FromSupervisor` and only added at a safe point, when the transcript ends with a user message, so it stays API-valid.
- If policy or the supervisor errors or times out, tools at `Config.FailClosedAt` risk or above are denied and lower-risk tools are allowed.
- Directives whose `AfterSeq` lags more than `supervise.Supervisor.MaxLag` events behind are dropped.
- Telemetry failures never block the loop.
