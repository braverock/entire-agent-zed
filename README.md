# entire-agent-zed

An external agent plugin for the [Entire.io CLI](https://github.com/entireio/cli) that provides transcript extraction for the [Zed Editor](https://zed.dev) AI Assistant.

## How it works

Zed stores all of its AI chat threads in a local SQLite database, compressing the payloads using `zstd`.

This agent operates as a **Database Tailer**:
1. When called, it locates the Zed database.
2. It queries the database for the most recently modified chat thread matching the current `ENTIRE_REPO_ROOT` environment variable.
3. It decompresses the `zstd` blob and parses the internal Zed JSON format (`User`, `Agent`, `ToolUse`).
4. It maps the chat into the standard Entire Transcript Schema and outputs it as JSON.

### Database Locations

| OS    | Path |
|-------|------|
| Linux | `~/.local/share/zed/threads/threads.db` |
| macOS | `~/Library/Application Support/Zed/threads/threads.db` |

## Capabilities

- `transcript_analyzer`: Extracts and parses Zed AI threads
- `transcript_preparer`: Maps Zed messages to Entire transcript schema
- `hooks`: Git hook lifecycle management (post-commit)
- `token_calculator`: Parses token usage from Zed's `request_token_usage` field

## Subcommands

| Subcommand | Description |
|---|---|
| `info` | Returns agent metadata and capabilities |
| `transcript` | Extracts the latest Zed thread as Entire transcript JSON |
| `parse-hook <event>` | Translates hook events into Entire lifecycle events |
| `install-hooks` | Installs a `post-commit` git hook for automatic lifecycle tracking |
| `uninstall-hooks` | Removes the hook (handles both legacy pre-commit and current post-commit) |
| `are-hooks-installed` | Checks if the hook is installed |
| `calculate-tokens` | Sums token usage from the latest thread |
| `attach` | Captures a research/planning thread that doesn't produce commits |
| `extract-branch` | Correlates Zed transcripts with the branch commit history |
| `get-session-dir` | Returns the directory containing Zed's threads.db |
| `resolve-session-file` | Returns the full path to threads.db |

## Hook Lifecycle (post-commit)

The agent installs a **post-commit** hook (not pre-commit) that fires lifecycle events after each commit:

```
[First commit only]
  session-start  →  Initializes the Entire session
  turn-start     →  Starts the first work unit

[Every commit]
  turn-end       →  Ends the current turn, capturing the transcript delta
  turn-start     →  Starts a new turn for the next work unit
```

This design ensures:
- The commit SHA exists when turn-end fires, enabling correlation
- Each "turn" represents the conversation between two consecutive commits
- Session ID is derived from the actual Zed thread ID (not a static string)
- The actual last user prompt is extracted from the thread (not a placeholder)
- A message count watermark is included for computing transcript deltas

## Attaching Research/Planning Threads

For Zed threads that don't produce any commits (research, planning, architecture discussions), use the `attach` subcommand:

```bash
entire-agent-zed attach
```

This fires the full lifecycle (session-start → turn-start → turn-end → session-end) in a single invocation, capturing the entire thread as one turn.

## Branch Timeline Extraction

The `extract-branch` subcommand correlates all Zed transcripts for a branch with its commit history:

```bash
# JSON output (default)
entire-agent-zed extract-branch

# Markdown report
entire-agent-zed extract-branch --format markdown

# Specific branch against a base
entire-agent-zed extract-branch --branch feature-x --base develop
```

### What it produces

The output is a timeline that reconstructs how AI-assisted development progressed across the branch:

- **Timeline entries** — one per commit, each showing which Zed threads were active around that commit (matched by a 30-minute time window)
- **Thread summaries** — a deduplicated list of all threads with message counts and token usage, reported once per thread (not duplicated across commits)
- **Token totals** — branch-level rollup of input, output, cache read tokens, and API call count across all threads
- **Orphan threads** — research/planning threads that were active for this repo but don't correlate with any commit (useful for capturing work that didn't result in code changes)

### Reconstructing thread history from a branch

To reconstruct the full AI conversation history for a feature branch:

```bash
# Step 1: Get the full timeline as JSON
entire-agent-zed extract-branch --branch feature-x --base main > timeline.json

# Step 2: Get a human-readable summary
entire-agent-zed extract-branch --branch feature-x --base main --format markdown > timeline.md
```

The JSON output has this structure:

```json
{
  "branch": "feature-x",
  "base": "main",
  "merge_base": "abc123...",
  "commit_count": 5,
  "thread_count": 2,
  "token_totals": {
    "input_tokens": 50000,
    "output_tokens": 12000,
    "cache_read_tokens": 30000,
    "api_call_count": 45
  },
  "thread_summaries": [
    {
      "thread_id": "uuid-1",
      "summary": "Implement auth module",
      "message_count": 42,
      "tokens": { "input_tokens": 30000, "output_tokens": 8000, "..." : "..." }
    }
  ],
  "timeline": [
    {
      "commit": { "sha": "def456", "timestamp": "...", "author": "...", "message": "feat: add login" },
      "files_changed": ["src/auth.go"],
      "diff_stats": "1 file changed, 50 insertions(+)",
      "threads": [
        { "thread_id": "uuid-1", "summary": "Implement auth module", "message_count": 42, "transcript": [...] }
      ]
    }
  ],
  "orphan_threads": [
    { "thread_id": "uuid-2", "summary": "Research OAuth providers", "message_count": 15, "transcript": [...] }
  ]
}
```

### Understanding token reporting

Token counts come from Zed's `request_token_usage` field in each thread's stored data. These are **cumulative totals for the entire thread**, not per-commit deltas. This is because:

- Zed stores the thread as a single blob — there are no per-message timestamps or per-turn token breakpoints in the database
- The same thread may be matched to multiple commits in the timeline (if the user was working in the same thread across several commits)

To avoid misleading duplication, tokens are reported at two levels:

| Level | What it shows |
|-------|---------------|
| `token_totals` | Sum across all unique threads for the branch |
| `thread_summaries[].tokens` | Per-thread cumulative totals (each thread counted once) |

Per-commit token deltas are **not** available retroactively. However, if the post-commit hooks are running, each `turn-end` event includes a token snapshot. The `entire` CLI can compute deltas from consecutive turn-end events in its own logs.

### Tips for analysis

- **Pipe JSON to `jq`** for ad-hoc queries: `entire-agent-zed extract-branch | jq '.timeline[] | {msg: .commit.message, threads: (.threads | length)}'`
- **Count AI-assisted vs. manual commits**: commits with zero matched threads were likely manual
- **Find the most expensive threads**: `jq '.thread_summaries | sort_by(-.tokens.input_tokens) | .[0]'`
- **Identify orphan research**: orphan threads reveal planning/investigation work that didn't directly produce code

## Upstream Entire.io CLI Compatibility

This agent currently exposes a limitation in `entire` v0.5.5 where external agent auto-discovery (`$PATH` scanning for `entire-agent-*` binaries) is filtered out for passive GUI agents. The recommended workaround is to call this binary directly from hooks or use the `install-hooks` subcommand.

## Building and Installing

```bash
make test      # Run all tests (40 tests)
make install   # Build and install to ~/.local/bin/entire-agent-zed
make uninstall # Remove from ~/.local/bin
make clean     # Remove local build artifact
```

The standard workflow after making changes:

```bash
make test && make install
```
