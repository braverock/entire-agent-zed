# Bug Report: `entire agent add/list` does not discover external agents

## Description
The `entire agent add <agent>`, `entire agent remove <agent>`, and `entire agent list` commands omit external agents (like `zed`), failing with `Unknown agent`. However, the exact same external agent works perfectly through `entire enable --agent <agent>` and the various git hook invocations.

## Root Cause
In `entireio-cli/cmd/entire/cli/agent_group.go`, the `agent` subcommand group (introduced in commit `c4109895`) does not invoke `external.DiscoverAndRegister(ctx)` or `external.DiscoverAndRegisterAlways(ctx)` prior to listing or retrieving agents.

By examining the CLI source codebase, we can see that discovery correctly occurs in most places where agents are retrieved. For instance:
- `entire enable`: calls `external.DiscoverAndRegisterAlways(ctx)` in `setup.go`.
- `entire hooks ...`: calls `external.DiscoverAndRegister(discoveryCtx)` in `hooks_cmd.go` and `hooks_git_cmd.go`.
- `entire attach`: calls `external.DiscoverAndRegister(cmd.Context())` in `attach.go`.
- `entire explain`: calls `external.DiscoverAndRegister(ctx)` in `explain.go`.

However, for the `entire agent add <name>` flow in `newAgentAddCmd()` (`agent_group.go`):

```go
RunE: func(cmd *cobra.Command, args []string) error {
    name := args[0]
    ag, err := agent.Get(types.AgentName(name)) // <-- Fails here, no discovery called!
    // ...
}
```

Since discovery is skipped entirely, `agent.Get()` only checks the statically registered built-in agents, throwing `Unknown agent` for any compliant external agent on the user's `$PATH`. The exact same omission happens in `newAgentRemoveCmd()` and `newAgentListCmd()`.

## Expected Behavior
The `entire agent` noun-group (`add`, `list`, `remove`) should discover external agents to comply with the external agent protocol. `entire agent add zed` should successfully install hooks for the `zed` agent just like `entire enable --agent zed` currently does.

## Suggested Fix
Call `external.DiscoverAndRegister(ctx)` or `external.DiscoverAndRegisterAlways(ctx)` in `runAgentMenu` (for `list`), and in `newAgentAddCmd`/`newAgentRemoveCmd` prior to invoking `agent.Get()`.

To reproduce the failure programmatically (and prove the fix), you can use the existing `writeExternalAgentBinary` test helper. If you write a test against `runAgentList` that creates a mock external agent and asserts it appears in the output, it will fail on `v0.9.0` but pass once the discovery calls are added:

```go
func TestAgentGroup_DiscoversExternalAgents(t *testing.T) {
	// Cannot use t.Parallel because we modify PATH via t.Setenv.
	externalDir := t.TempDir()
	writeExternalAgentBinary(t, externalDir, "ext-agentgroup-test")
	t.Setenv("PATH", externalDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var buf bytes.Buffer
	if err := runAgentList(context.Background(), &buf); err != nil {
		t.Fatalf("runAgentList with external: %v", err)
	}
	if !strings.Contains(buf.String(), "ext-agentgroup-test") {
		t.Errorf("expected external agent 'ext-agentgroup-test' in output, got:\n%s", buf.String())
	}
}
```

A complete patch implementing both the fix and this test is included in this report as `0001-fix-agent-group-omits-external-agent-discovery.patch`.