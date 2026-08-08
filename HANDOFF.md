# Handoff: Zed Agent Integration Testing

This document hands off the state of the `entire-agent-zed` plugin work to a Zed agent
so it can continue integration testing. It assumes the reader has no memory of the
prior session.

## Goal

Make the `entire` CLI (v0.9.0) recognize and correctly drive the `entire-agent-zed`
external agent plugin. Two symptoms were originally reported:

1. `entire agent add zed` fails with `Unknown agent "zed"`; `entire agent list`
   omits Zed entirely.
2. Zed lifecycle hooks did not appear to fire / transcript data was empty.

## What has been done (completed)

### Plugin brought to protocol compliance

`main.go` in this repo now implements every subcommand the external-agent protocol
requires, plus the optional ones the CLI calls for capability-declared agents:

- Required: `info`, `detect`, `get-session-id`, `read-session`, `write-session`,
  `read-transcript`, `chunk-transcript`, `reassemble-transcript`,
  `format-resume-command`, `get-transcript-position`, `extract-modified-files`,
  `extract-prompts`, `extract-summary`, `prepare-transcript`.
- `info` now emits `hook_names: ["session-start","turn-start","turn-end","session-end"]`.
- Capabilities declared: `transcript_analyzer`, `transcript_preparer`, `hooks`,
  `token_calculator` (all required subcommands for these now exist).

### Verification performed

- `go build` clean; `go test -count=1 ./...` passes (30s).
- 14 new binary tests added in `main_test.go` covering all new subcommands.
- `make install` run; `~/.local/bin/entire-agent-zed` is the current build.
- `entire hooks zed <event>` (the discovery path that DOES call `detect`) works:
  - `session-start`, `turn-start`, `turn-end`, `session-end` all exit 0.
  - `.entire/logs/entire.log` shows zed lifecycle events firing.
  - `turn-end` produces a checkpoint and writes `.entire/metadata/zed-*/full.jsonl`
    and `prompt.txt`.

## What is still broken / unresolved

### 1. `entire agent add/list` do not discover external agents (CLI-side bug)

`entire agent add zed` still errors `Unknown agent "zed"` and `agent list` omits
Zed. Root cause is in the CLI source (not this repo):

- `/tmp/opencode/entireio-cli/cmd/entire/cli/agent_group.go` — the `add`/`list`/
  `remove` noun-group was introduced in commit `c4109895` and NEVER calls external
  agent discovery. Only the hook path (`entire hooks zed <event>`) and
  `entire enable --agent zed` use discovery.
- `detect` and `hook_names` have been required by the protocol since the initial
  commit `74114ec2`.

Per the user's decision: do NOT patch the CLI locally. The plugin is compliant and
functional; the remaining gap is a CLI bug to report upstream. Only if the plugin
were still broken while compliant would we craft a CLI patch.

### 2. Transcript appears empty (`{}`) under the CLI's environment

When the CLI runs `read-transcript --session-ref <dbPath>`, it sets
`ENTIRE_REPO_ROOT=<repo>`. `fetchLatestThread` (main.go:284) then filters threads
by `folder_paths LIKE '%<repo>%'`. If no Zed thread exists for that repo, the query
returns no rows and the plugin outputs `{}` (exit 0, non-fatal).

This is CORRECT protocol behavior — there is simply no Zed session for the repo yet.
Once a real Zed session is active in the repo, the transcript will populate.

### 3. Git hook wiring (already resolved, but note the mechanism)

`entire enable --agent zed` reports "Hooks for Zed AI Assistant already installed"
and does NOT inline the zed block into `.git/hooks/post-commit`. Instead it installs
a generic post-commit that chains to `.git/hooks/post-commit.pre-entire`, which
contains the "Entire Zed Agent Lifecycle Hook" block. This is the intended chaining
mechanism — the hook IS installed.

## Next steps for the Zed agent

1. **Start a real Zed session in this repo** (or a scratch repo) so a thread with
   `folder_paths` matching the repo exists. Make a commit.
2. **Verify the hook fires**: check `.entire/logs/entire.log` for zed
   `session-start`/`turn-start`/`turn-end` events and confirm
   `.entire/metadata/zed-*/full.jsonl` is populated (not `{}`).
3. **Verify `entire hooks zed <event>` directly**:
   `echo '{"session_id":"test"}' | entire hooks zed turn-start`.
4. **Confirm the CLI-side gap** is unchanged: `entire agent add zed` still fails.
5. **Report the CLI bug upstream** (works-but-unlisted case): `agent add/list`
   never discover external agents. Reference `agent_group.go` and the protocol doc.

## Useful paths

- Plugin repo: `/home/brian/mule_dev/R/entire-agent-zed`
- Plugin binary: `~/.local/bin/entire-agent-zed` (rebuilt via `make install`)
- CLI source: `/tmp/opencode/entireio-cli` (tag v0.9.0 = commit `8b77ad4`)
- Protocol spec: `/tmp/opencode/entireio-cli/docs/architecture/external-agent-protocol.md`
- Zed DB: `~/.local/share/zed/threads/threads.db`
- Logs: `.entire/logs/entire.log`
- Session metadata: `.entire/metadata/zed-*/`