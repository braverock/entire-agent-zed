# entire-agent-zed

This is an external agent plugin for the [Entire.io CLI](https://github.com/entireio/cli) that provides transcript extraction for the [Zed Editor](https://zed.dev) AI Assistant.

## How it works

Zed stores all of its AI chat threads in a local SQLite database (`~/.local/share/zed/threads/threads.db`), compressing the payloads using `zstd`.

This agent operates as a **Database Tailer**:
1. When called, it locates the Zed database.
2. It queries the database for the most recently modified chat thread matching the current `ENTIRE_REPO_ROOT` environment variable.
3. It decompresses the `zstd` blob and parses the internal Zed JSON format (`User`, `Agent`, `ToolUse`).
4. It maps the chat into the standard Entire Transcript Schema and outputs it as JSON.

## Current Capabilities
- `transcript_analyzer`: true
- `transcript_preparer`: true
- `hooks`: true (Mocked endpoints for CLI compatibility)

## Planned Capabilities (Pending Upstream Fixes)
- `token_calculator`: Parse token usage metrics out of Zed's database schema.
- `subagent_aware_extractor`: Support Zed's multi-file or multi-agent context blocks.

*Note: These extra capabilities are deferred until the `entire` CLI fully recognizes and registers passive external GUI agents.*

## Upstream Entire.io CLI Compatibility

This agent currently exposes a limitation/bug in `entire` v0.5.5 where external agent auto-discovery (`$PATH` scanning for `entire-agent-*` binaries) is:
1. Filtered out of `entire configure` interactive menus if the agent is passive (doesn't trigger its own start/stop hooks).
2. Explicitly bypassed in commands like `entire attach --agent zed` due to a hardcoded allowlist.

### Current Workaround
Because the Entire CLI does not currently auto-invoke this binary natively during standard git hooks, the recommended workaround is to call this binary directly from a custom git hook or wrapper script, passing the parsed JSON output into Entire.