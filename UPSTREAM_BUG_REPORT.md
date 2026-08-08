# Bug Report: `entire agent add/list` does not discover external agents

## Description
The `entire agent add <agent>` and `entire agent list` commands omit external agents (like `zed`), failing with `Unknown agent`. However, the external agent works perfectly through `entire enable --agent <agent>` and git hooks.

## Root Cause
In `entireio-cli/cmd/entire/cli/agent_group.go`, the `agent` subcommand group (introduced in commit `c4109895`) does not invoke `external.DiscoverAndRegister(ctx)` or `external.DiscoverAndRegisterAlways(ctx)` prior to listing or retrieving agents.

For instance, in `newAgentAddCmd()`:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    name := args[0]
    ag, err := agent.Get(types.AgentName(name))
    // ...
}
```

Since discovery is skipped, `agent.Get()` only checks the statically registered agents, throwing `Unknown agent` for any compliant external agent.

In contrast, `newEnableCmd` correctly performs discovery before retrieving the agent:
```go
// From setup.go:newEnableCmd
external.DiscoverAndRegisterAlways(ctx)
// ...
if agentName != "" {
    ag, err := agent.Get(types.AgentName(agentName))
```

## Expected Behavior
The `entire agent` noun-group (`add`, `list`, `remove`) should discover external agents to comply with the external agent protocol. `entire agent add zed` should successfully install hooks for the `zed` agent.

## Suggested Fix
Call `external.DiscoverAndRegister(ctx)` in `runAgentMenu` (for `list`) and in `newAgentAddCmd`/`newAgentRemoveCmd` prior to invoking `agent.Get()`.