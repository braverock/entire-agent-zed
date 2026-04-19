package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "github.com/mattn/go-sqlite3"
)

// Thread represents a Zed assistant thread from the SQLite database
type Thread struct {
	ID          string
	Summary     string
	UpdatedAt   string
	DataType    string
	Data        []byte
	FolderPaths string
}

type EntireMessage struct {
	Role    string       `json:"role"`
	Content string       `json:"content"`
	Tools   []EntireTool `json:"tools,omitempty"`
}

type EntireTool struct {
	Name   string                 `json:"name"`
	Params map[string]interface{} `json:"params"`
}

type CommitInfo struct {
	SHA       string `json:"sha"`
	Timestamp string `json:"timestamp"`
	Author    string `json:"author"`
	Message   string `json:"message"`
}

type TimelineEntry struct {
	Commit       CommitInfo    `json:"commit"`
	FilesChanged []string      `json:"files_changed"`
	DiffStats    string        `json:"diff_stats"`
	Threads      []interface{} `json:"threads"`
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	subcommand := os.Args[1]

	switch subcommand {
	case "help", "--help", "-h":
		printHelp()
	case "info":
		handleInfo()
	case "transcript":
		handleTranscript()
	case "start":
		handleStart()
	case "stop":
		handleStop()
	case "parse-hook":
		handleParseHook()
	case "get-session-dir":
		handleGetSessionDir()
	case "resolve-session-file":
		handleResolveSessionFile()
	case "install-hooks":
		handleInstallHooks()
	case "uninstall-hooks":
		handleUninstallHooks()
	case "are-hooks-installed":
		handleAreHooksInstalled()
	case "calculate-tokens":
		handleCalculateTokens()
	case "attach":
		handleAttach()
	case "extract-branch":
		handleExtractBranch()
	case "end-session":
		handleEndSession()
	default:
		// Required by the Entire External Agent Protocol: Exit gracefully if unsupported
		fmt.Println("{}")
		os.Exit(0)
	}
}

func printHelp() {
	fmt.Print(`entire-agent-zed — Entire External Agent plugin for Zed Editor

Usage: entire-agent-zed <command> [options]

Hook management:
  install-hooks          Install post-commit git hooks for automatic lifecycle tracking
                         Hooks fire: session-start → turn-start on first commit,
                         then turn-end → turn-start on each subsequent commit.
                         Migrates legacy pre-commit hooks automatically.
                         Uses $ENTIRE_REPO_ROOT or auto-detects via git.

  uninstall-hooks        Remove installed git hooks (both post-commit and legacy pre-commit)

  are-hooks-installed    Check whether hooks are installed (exits with JSON: {"installed": true/false})

User commands:
  attach                 Capture a research/planning thread that has no commits.
                         Fires the full lifecycle in one shot:
                         session-start → turn-start → turn-end → session-end.
                         Also writes a session snapshot for cross-agent handoff.

  end-session            Manually end the current Zed session.
                         Fires turn-end → session-end and writes a session snapshot.

  extract-branch         Extract all Zed transcripts correlated with branch commits.
                         Matches threads to commits by timestamp (±30min window).
                         Reports deduplicated token usage per-thread and branch totals.
    --branch <name>        Branch to extract (default: current)
    --base <name>          Base branch for merge-base (default: main)
    --format json|markdown Output format (default: json)

  transcript             Extract the latest Zed thread transcript as JSON.
                         Filters by $ENTIRE_REPO_ROOT if set.

  calculate-tokens       Report cumulative token usage for the latest thread.

Protocol commands (called by the Entire CLI — not typically run directly):
  info                   Print agent capabilities as JSON
  parse-hook <hook>      Parse a lifecycle hook event. Reads JSON payload from stdin.
                         Valid hooks: session-start, turn-start, turn-end, session-end
                         On turn-end/session-end: includes token snapshot and writes
                         session file to .git/entire-sessions/ for handoff.
  start                  No-op start signal (protocol requirement)
  stop                   No-op stop signal (protocol requirement)
  get-session-dir        Print the Zed threads directory path
  resolve-session-file   Print the Zed threads database path

  help, --help, -h       Print this help message

Environment:
  ENTIRE_REPO_ROOT       Git repo root (auto-detected if unset)
  Zed DB location        Linux:  ~/.local/share/zed/threads/threads.db
                         macOS:  ~/Library/Application Support/Zed/threads/threads.db
`)
}

func handleInfo() {
	info := map[string]interface{}{
		"protocol_version": 1,
		"name":             "zed",
		"type":             "Zed",
		"description":      "Zed AI Assistant",
		"capabilities": map[string]bool{
			"transcript_analyzer": true,
			"transcript_preparer": true,
			"hooks":               true,
			"token_calculator":    true,
		},
	}
	json.NewEncoder(os.Stdout).Encode(info)
}

func fetchLatestThread(db *sql.DB) (*Thread, []byte, error) {
	var t Thread
	var folderPaths sql.NullString

	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	var row *sql.Row

	if repoRoot != "" {
		query := `
			SELECT id, summary, updated_at, data_type, data, folder_paths
			FROM threads
			WHERE folder_paths LIKE ?
			ORDER BY updated_at DESC
			LIMIT 1
		`
		row = db.QueryRow(query, "%"+repoRoot+"%")
	} else {
		row = db.QueryRow(`
			SELECT id, summary, updated_at, data_type, data, folder_paths
			FROM threads
			ORDER BY updated_at DESC
			LIMIT 1
		`)
	}

	err := row.Scan(&t.ID, &t.Summary, &t.UpdatedAt, &t.DataType, &t.Data, &folderPaths)
	if err != nil {
		return nil, nil, err
	}
	if folderPaths.Valid {
		t.FolderPaths = folderPaths.String
	}

	var rawData []byte
	if t.DataType == "zstd" {
		decoder, err := zstd.NewReader(bytes.NewReader(t.Data))
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize ZSTD decoder: %v", err)
		}
		defer decoder.Close()

		rawData, err = io.ReadAll(decoder)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decompress ZSTD blob: %v", err)
		}
	} else {
		rawData = t.Data
	}

	return &t, rawData, nil
}

func parseTranscript(rawData []byte) ([]EntireMessage, error) {
	var parsed map[string]interface{}
	if err := json.Unmarshal(rawData, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse Zed thread JSON: %v", err)
	}

	messagesRaw, ok := parsed["messages"].([]interface{})
	if !ok {
		return []EntireMessage{}, nil
	}

	var transcript []EntireMessage

	for _, mRaw := range messagesRaw {
		m, ok := mRaw.(map[string]interface{})
		if !ok {
			continue
		}

		var role string
		var contentRaw interface{}

		if user, exists := m["User"]; exists {
			role = "user"
			contentRaw = user
		} else if agent, exists := m["Agent"]; exists {
			role = "assistant"
			contentRaw = agent
		} else {
			continue
		}

		cMap, ok := contentRaw.(map[string]interface{})
		if !ok {
			continue
		}

		contentList, ok := cMap["content"].([]interface{})
		if !ok {
			continue
		}

		var textContent string
		var tools []EntireTool

		for _, itemRaw := range contentList {
			item, ok := itemRaw.(map[string]interface{})
			if !ok {
				continue
			}

			if text, ok := item["Text"].(string); ok {
				textContent += text
			} else if mention, ok := item["Mention"].(map[string]interface{}); ok {
				if mc, ok := mention["content"].(string); ok {
					textContent += mc
				}
			} else if toolUse, ok := item["ToolUse"].(map[string]interface{}); ok {
				name, _ := toolUse["name"].(string)
				input, _ := toolUse["input"].(map[string]interface{})
				if name != "" {
					tools = append(tools, EntireTool{
						Name:   name,
						Params: input,
					})
				}
			}
		}

		transcript = append(transcript, EntireMessage{
			Role:    role,
			Content: textContent,
			Tools:   tools,
		})
	}

	return transcript, nil
}

func handleTranscript() {
	dbPath, err := getZedDBPath()
	if err != nil {
		log.Fatalf("Could not find Zed database: %v", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro") // Open read-only
	if err != nil {
		log.Fatalf("Could not open database: %v", err)
	}
	defer db.Close()

	_, rawData, err := fetchLatestThread(db)
	if err != nil {
		if err == sql.ErrNoRows {
			fmt.Println("[]") // No transcript available
			os.Exit(0)
		}
		log.Fatalf("Query error: %v", err)
	}

	transcript, err := parseTranscript(rawData)
	if err != nil {
		log.Fatalf("%v", err)
	}

	out, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal entire transcript: %v", err)
	}
	fmt.Printf("%s\n", out)
}

func getZedDBPath() (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", err
	}

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			filepath.Join(usr.HomeDir, "Library", "Application Support", "Zed", "threads", "threads.db"),
		}
	default: // linux
		candidates = []string{
			filepath.Join(usr.HomeDir, ".local", "share", "zed", "threads", "threads.db"),
		}
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("Zed threads database not found (checked %v)", candidates)
}

func handleStart() {
	// The protocol expects a successful exit for start
	fmt.Println("{}")
}

func handleStop() {
	// The protocol expects a successful exit for stop
	fmt.Println("{}")
}

// getLatestThreadMeta returns the thread ID, last user message, message count,
// and raw decompressed data from the Zed DB.
// Returns empty/nil on any error (best-effort).
func getLatestThreadMeta() (threadID string, lastUserMsg string, msgCount int, rawData []byte) {
	dbPath, err := getZedDBPath()
	if err != nil {
		return "", "", 0, nil
	}
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return "", "", 0, nil
	}
	defer db.Close()

	t, raw, err := fetchLatestThread(db)
	if err != nil {
		return "", "", 0, nil
	}

	transcript, err := parseTranscript(raw)
	if err != nil {
		return t.ID, "", 0, raw
	}

	// Find the last user message
	for i := len(transcript) - 1; i >= 0; i-- {
		if transcript[i].Role == "user" && strings.TrimSpace(transcript[i].Content) != "" {
			lastUserMsg = transcript[i].Content
			break
		}
	}

	// Truncate long prompts for the event payload
	if len(lastUserMsg) > 500 {
		lastUserMsg = lastUserMsg[:500] + "..."
	}

	return t.ID, lastUserMsg, len(transcript), raw
}

func handleParseHook() {
	hookName := ""
	for i, arg := range os.Args {
		if arg == "--hook" && i+1 < len(os.Args) {
			hookName = os.Args[i+1]
			break
		}
	}
	if hookName == "" && len(os.Args) >= 3 && os.Args[2] != "--hook" {
		hookName = os.Args[2]
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil && err != io.EOF {
		// Proceed with empty payload
	}

	sessionID := ""
	if sid, ok := payload["session_id"].(string); ok && sid != "" {
		sessionID = sid
	}

	// Derive session ID and prompt from the actual Zed thread
	threadID, lastUserMsg, msgCount, rawData := getLatestThreadMeta()
	if sessionID == "" {
		if threadID != "" {
			sessionID = "zed-" + threadID
		} else {
			sessionID = "zed-current"
		}
	}

	var eventType int
	if hookName == "session-start" || hookName == "start" {
		eventType = 1 // agent.SessionStart
	} else if hookName == "turn-start" {
		eventType = 2 // agent.TurnStart
	} else if hookName == "turn-end" {
		eventType = 3 // agent.TurnEnd
	} else if hookName == "stop" || hookName == "session-end" {
		eventType = 5 // agent.SessionEnd
	} else {
		eventType = 0 // Unknown
	}

	dbPath, _ := getZedDBPath()
	if dbPath == "" {
		dbPath = "zed-db"
	}

	out := map[string]interface{}{
		"type":        eventType,
		"session_id":  sessionID,
		"session_ref": dbPath,
		"timestamp":   time.Now().Format(time.RFC3339),
	}

	// TurnStart events require a Prompt string in the JSON payload
	if eventType == 2 {
		if lastUserMsg != "" {
			out["prompt"] = lastUserMsg
		} else {
			out["prompt"] = "Zed AI prompt"
		}
	}

	// Include message count as a watermark for delta calculations
	if msgCount > 0 {
		out["message_count"] = msgCount
	}

	// Include token usage on turn-end and session-end events
	if (eventType == 3 || eventType == 5) && rawData != nil {
		if tokens := extractTokens(rawData); tokens != nil {
			out["tokens"] = tokens
		}
	}

	// Write transcript snapshot on turn-end and session-end for session-handoff skill
	if eventType == 3 || eventType == 5 {
		phase := "active"
		if eventType == 5 {
			phase = "ended"
		}
		writeSessionSnapshot(sessionID, threadID, lastUserMsg, phase, rawData)
	}

	json.NewEncoder(os.Stdout).Encode(out)
}

// writeSessionSnapshot writes a session JSON file and transcript to
// .git/entire-sessions/ so that the session-handoff skill can find and
// read Zed transcripts for cross-agent handoff.
func writeSessionSnapshot(sessionID, threadID, lastPrompt, phase string, rawData []byte) {
	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	if repoRoot == "" {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return // not in a git repo
		}
		repoRoot = strings.TrimSpace(string(out))
	}

	sessionsDir := filepath.Join(repoRoot, ".git", "entire-sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return
	}

	// Write the transcript file (the raw Entire-format messages)
	transcriptPath := filepath.Join(sessionsDir, sessionID+".transcript.json")
	if rawData != nil {
		transcript, err := parseTranscript(rawData)
		if err == nil && len(transcript) > 0 {
			data, err := json.MarshalIndent(transcript, "", "  ")
			if err == nil {
				os.WriteFile(transcriptPath, data, 0644)
			}
		}
	}

	// Write the session metadata JSON (what session-handoff reads first)
	now := time.Now().Format(time.RFC3339)
	sessionMeta := map[string]interface{}{
		"session_id":          sessionID,
		"agent_type":          "Zed",
		"phase":               phase,
		"started_at":          now,
		"last_interaction_time": now,
		"transcript_path":     transcriptPath,
	}
	if lastPrompt != "" {
		sessionMeta["last_prompt"] = lastPrompt
	}
	if threadID != "" {
		sessionMeta["thread_id"] = threadID
	}

	metaData, err := json.MarshalIndent(sessionMeta, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(sessionsDir, sessionID+".json"), metaData, 0644)
}

// handleAttach captures a research/planning thread that doesn't produce commits.
// It fires the full lifecycle: session-start → turn-start → turn-end → session-end
// in a single invocation, using the Entire CLI.
func handleAttach() {
	threadID, lastUserMsg, msgCount, rawData := getLatestThreadMeta()

	if threadID == "" {
		fmt.Fprintf(os.Stderr, "Error: no Zed thread found for this repository\n")
		os.Exit(1)
	}

	sessionID := "zed-" + threadID

	// Try to invoke `entire` CLI to fire the lifecycle events
	entireCmd := "entire"
	if _, err := exec.LookPath("entire-patched"); err == nil {
		entireCmd = "entire-patched"
	}

	// Fire session-start
	runEntireHook(entireCmd, "session-start", sessionID)

	// Fire turn-start with the actual prompt
	runEntireHook(entireCmd, "turn-start", sessionID)

	// Fire turn-end
	runEntireHook(entireCmd, "turn-end", sessionID)

	// Fire session-end
	runEntireHook(entireCmd, "session-end", sessionID)

	// Write session snapshot for cross-agent handoff
	writeSessionSnapshot(sessionID, threadID, lastUserMsg, "ended", rawData)

	summary := map[string]interface{}{
		"status":        "attached",
		"session_id":    sessionID,
		"thread_id":     threadID,
		"message_count": msgCount,
	}
	if lastUserMsg != "" {
		summary["last_prompt"] = lastUserMsg
	}

	out, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Printf("%s\n", out)
}

// handleEndSession manually ends the current Zed session from the command line.
// Fires turn-end → session-end via the Entire CLI and writes a session snapshot.
func handleEndSession() {
	threadID, lastUserMsg, _, rawData := getLatestThreadMeta()

	if threadID == "" {
		fmt.Fprintf(os.Stderr, "Error: no Zed thread found for this repository\n")
		os.Exit(1)
	}

	sessionID := "zed-" + threadID

	entireCmd := "entire"
	if _, err := exec.LookPath("entire-patched"); err == nil {
		entireCmd = "entire-patched"
	}

	// End the current turn, then end the session
	runEntireHook(entireCmd, "turn-end", sessionID)
	runEntireHook(entireCmd, "session-end", sessionID)

	// Write session snapshot for cross-agent handoff
	writeSessionSnapshot(sessionID, threadID, lastUserMsg, "ended", rawData)

	summary := map[string]interface{}{
		"status":     "ended",
		"session_id": sessionID,
		"thread_id":  threadID,
	}
	if lastUserMsg != "" {
		summary["last_prompt"] = lastUserMsg
	}

	out, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Printf("%s\n", out)
}

func runEntireHook(entireCmd, hookName, sessionID string) {
	payload := map[string]interface{}{
		"session_id": sessionID,
	}
	payloadJSON, _ := json.Marshal(payload)

	cmd := exec.Command(entireCmd, "hooks", "zed", hookName)
	cmd.Stdin = bytes.NewReader(payloadJSON)
	cmd.Stdout = os.Stderr // Send entire CLI output to stderr so it doesn't pollute our JSON
	cmd.Stderr = os.Stderr
	cmd.Run() // Best-effort; ignore errors
}

// handleExtractBranch extracts all Zed transcripts for the current branch
// and correlates them with the commit history.
func handleExtractBranch() {
	// Parse flags
	branch := ""
	baseBranch := "main"
	format := "json"
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--branch":
			if i+1 < len(os.Args) {
				branch = os.Args[i+1]
				i++
			}
		case "--base":
			if i+1 < len(os.Args) {
				baseBranch = os.Args[i+1]
				i++
			}
		case "--format":
			if i+1 < len(os.Args) {
				format = os.Args[i+1]
				i++
			}
		}
	}

	// Get current branch if not specified
	if branch == "" {
		out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			log.Fatalf("Failed to get current branch: %v", err)
		}
		branch = strings.TrimSpace(string(out))
	}

	// Get merge-base
	mergeBase, err := exec.Command("git", "merge-base", baseBranch, branch).Output()
	if err != nil {
		// If no merge-base, use the first commit
		mergeBase, err = exec.Command("git", "rev-list", "--max-parents=0", branch).Output()
		if err != nil {
			log.Fatalf("Failed to find merge-base: %v", err)
		}
	}
	mergeBaseSHA := strings.TrimSpace(string(mergeBase))

	// Get commit log from merge-base to HEAD
	logOut, err := exec.Command("git", "log", "--format=%H|%aI|%an|%s", "--reverse",
		mergeBaseSHA+".."+branch).Output()
	if err != nil {
		log.Fatalf("Failed to get git log: %v", err)
	}

	var commits []CommitInfo
	for _, line := range strings.Split(strings.TrimSpace(string(logOut)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, CommitInfo{
			SHA:       parts[0],
			Timestamp: parts[1],
			Author:    parts[2],
			Message:   parts[3],
		})
	}

	// Open Zed DB
	dbPath, err := getZedDBPath()
	if err != nil {
		log.Fatalf("Could not find Zed database: %v", err)
	}
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		log.Fatalf("Could not open database: %v", err)
	}
	defer db.Close()

	// Fetch all threads for this repo
	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	if repoRoot == "" {
		// Try to detect repo root
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err == nil {
			repoRoot = strings.TrimSpace(string(out))
		}
	}

	threads, err := fetchAllThreads(db, repoRoot)
	if err != nil {
		log.Fatalf("Failed to fetch threads: %v", err)
	}

	var timeline []TimelineEntry

	for _, commit := range commits {
		// Get files changed in this commit
		filesOut, _ := exec.Command("git", "diff-tree", "--no-commit-id", "-r", "--name-only", commit.SHA).Output()
		var files []string
		for _, f := range strings.Split(strings.TrimSpace(string(filesOut)), "\n") {
			if f != "" {
				files = append(files, f)
			}
		}

		// Get diff stats
		statsOut, _ := exec.Command("git", "diff-tree", "--no-commit-id", "--stat", commit.SHA).Output()
		diffStats := strings.TrimSpace(string(statsOut))

		// Find threads updated around this commit time
		commitTime, _ := time.Parse(time.RFC3339, commit.Timestamp)
		var matchingThreads []interface{}

		for _, t := range threads {
			threadTime, terr := time.Parse("2006-01-02 15:04:05", t.UpdatedAt)
			if terr != nil {
				threadTime, _ = time.Parse(time.RFC3339, t.UpdatedAt)
			}

			// Match threads updated within a reasonable window of the commit
			// (30 minutes before the commit is a reasonable heuristic)
			if threadTime.After(commitTime.Add(-30*time.Minute)) && threadTime.Before(commitTime.Add(5*time.Minute)) {
				transcript, _ := parseTranscript(t.DecompressedData)

				entry := map[string]interface{}{
					"thread_id":     t.ID,
					"summary":       t.Summary,
					"updated_at":    t.UpdatedAt,
					"message_count": len(transcript),
					"transcript":    transcript,
				}
				matchingThreads = append(matchingThreads, entry)
			}
		}

		timeline = append(timeline, TimelineEntry{
			Commit:       commit,
			FilesChanged: files,
			DiffStats:    diffStats,
			Threads:      matchingThreads,
		})
	}

	// Also include threads that don't match any commit (research/planning threads)
	var orphanThreads []interface{}
	for _, t := range threads {
		matched := false
		threadTime, terr := time.Parse("2006-01-02 15:04:05", t.UpdatedAt)
		if terr != nil {
			threadTime, _ = time.Parse(time.RFC3339, t.UpdatedAt)
		}

		for _, commit := range commits {
			commitTime, _ := time.Parse(time.RFC3339, commit.Timestamp)
			if threadTime.After(commitTime.Add(-30*time.Minute)) && threadTime.Before(commitTime.Add(5*time.Minute)) {
				matched = true
				break
			}
		}

		if !matched {
			transcript, _ := parseTranscript(t.DecompressedData)
			tokens := extractTokens(t.DecompressedData)
			entry := map[string]interface{}{
				"thread_id":     t.ID,
				"summary":       t.Summary,
				"updated_at":    t.UpdatedAt,
				"message_count": len(transcript),
				"transcript":    transcript,
			}
			if tokens != nil {
				entry["tokens"] = tokens
			}
			orphanThreads = append(orphanThreads, entry)
		}
	}

	// Build per-thread token summary (deduplicated, reported once per thread)
	var threadSummaries []interface{}
	totalInput, totalOutput, totalCacheRead, totalAPICalls := 0, 0, 0, 0

	for _, t := range threads {
		transcript, _ := parseTranscript(t.DecompressedData)
		tokens := extractTokens(t.DecompressedData)

		summary := map[string]interface{}{
			"thread_id":     t.ID,
			"summary":       t.Summary,
			"updated_at":    t.UpdatedAt,
			"message_count": len(transcript),
		}
		if tokens != nil {
			summary["tokens"] = tokens
			if v, ok := tokens["input_tokens"].(int); ok {
				totalInput += v
			}
			if v, ok := tokens["output_tokens"].(int); ok {
				totalOutput += v
			}
			if v, ok := tokens["cache_read_tokens"].(int); ok {
				totalCacheRead += v
			}
			if v, ok := tokens["api_call_count"].(int); ok {
				totalAPICalls += v
			}
		}
		threadSummaries = append(threadSummaries, summary)
	}

	tokenTotals := map[string]interface{}{
		"input_tokens":      totalInput,
		"output_tokens":     totalOutput,
		"cache_read_tokens": totalCacheRead,
		"api_call_count":    totalAPICalls,
	}

	result := map[string]interface{}{
		"branch":           branch,
		"base":             baseBranch,
		"merge_base":       mergeBaseSHA,
		"commit_count":     len(commits),
		"thread_count":     len(threads),
		"token_totals":     tokenTotals,
		"thread_summaries": threadSummaries,
		"timeline":         timeline,
		"orphan_threads":   orphanThreads,
	}

	if format == "markdown" {
		printMarkdownTimeline(result, timeline, orphanThreads)
	} else {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Printf("%s\n", out)
	}
}

type ThreadWithData struct {
	ID               string
	Summary          string
	UpdatedAt        string
	FolderPaths      string
	DecompressedData []byte
}

func fetchAllThreads(db *sql.DB, repoRoot string) ([]ThreadWithData, error) {
	var rows *sql.Rows
	var err error

	if repoRoot != "" {
		rows, err = db.Query(`
			SELECT id, summary, updated_at, data_type, data, folder_paths
			FROM threads
			WHERE folder_paths LIKE ?
			ORDER BY updated_at DESC
		`, "%"+repoRoot+"%")
	} else {
		rows, err = db.Query(`
			SELECT id, summary, updated_at, data_type, data, folder_paths
			FROM threads
			ORDER BY updated_at DESC
		`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []ThreadWithData
	for rows.Next() {
		var id, summary, updatedAt, dataType string
		var data []byte
		var folderPaths sql.NullString

		if err := rows.Scan(&id, &summary, &updatedAt, &dataType, &data, &folderPaths); err != nil {
			continue
		}

		var rawData []byte
		if dataType == "zstd" {
			decoder, err := zstd.NewReader(bytes.NewReader(data))
			if err != nil {
				continue
			}
			rawData, err = io.ReadAll(decoder)
			decoder.Close()
			if err != nil {
				continue
			}
		} else {
			rawData = data
		}

		fp := ""
		if folderPaths.Valid {
			fp = folderPaths.String
		}

		threads = append(threads, ThreadWithData{
			ID:               id,
			Summary:          summary,
			UpdatedAt:        updatedAt,
			FolderPaths:      fp,
			DecompressedData: rawData,
		})
	}

	return threads, nil
}

func extractTokens(rawData []byte) map[string]interface{} {
	var parsed map[string]interface{}
	if err := json.Unmarshal(rawData, &parsed); err != nil {
		return nil
	}

	usage, ok := parsed["request_token_usage"].(map[string]interface{})
	if !ok {
		return nil
	}

	var inputTokens, outputTokens, cacheReadTokens, apiCallCount int
	for _, reqRaw := range usage {
		if req, ok := reqRaw.(map[string]interface{}); ok {
			apiCallCount++
			if in, ok := req["input_tokens"].(float64); ok {
				inputTokens += int(in)
			}
			if out, ok := req["output_tokens"].(float64); ok {
				outputTokens += int(out)
			}
			if cr, ok := req["cache_read_input_tokens"].(float64); ok {
				cacheReadTokens += int(cr)
			}
		}
	}

	if apiCallCount == 0 {
		return nil
	}

	return map[string]interface{}{
		"input_tokens":      inputTokens,
		"output_tokens":     outputTokens,
		"cache_read_tokens": cacheReadTokens,
		"api_call_count":    apiCallCount,
	}
}

func printMarkdownTimeline(result map[string]interface{}, timeline []TimelineEntry, orphanThreads []interface{}) {
	branch := result["branch"].(string)
	base := result["base"].(string)

	fmt.Printf("# Branch Timeline: %s (from %s)\n\n", branch, base)
	fmt.Printf("**Commits:** %d | **Threads:** %d\n\n", result["commit_count"], result["thread_count"])

	// Token summary
	if totals, ok := result["token_totals"].(map[string]interface{}); ok {
		if totals["api_call_count"] != 0 {
			fmt.Printf("### Token Usage (all threads)\n\n")
			fmt.Printf("| Metric | Count |\n")
			fmt.Printf("|--------|-------|\n")
			fmt.Printf("| Input tokens | %v |\n", totals["input_tokens"])
			fmt.Printf("| Output tokens | %v |\n", totals["output_tokens"])
			fmt.Printf("| Cache read tokens | %v |\n", totals["cache_read_tokens"])
			fmt.Printf("| API calls | %v |\n\n", totals["api_call_count"])
		}
	}

	for i, entry := range timeline {
		fmt.Printf("## %d. %s\n\n", i+1, entry.Commit.Message)
		sha := entry.Commit.SHA
		if len(sha) > 8 {
			sha = sha[:8]
		}
		fmt.Printf("- **SHA:** `%s`\n", sha)
		fmt.Printf("- **Time:** %s\n", entry.Commit.Timestamp)
		fmt.Printf("- **Author:** %s\n", entry.Commit.Author)

		if len(entry.FilesChanged) > 0 {
			fmt.Printf("- **Files:** %s\n", strings.Join(entry.FilesChanged, ", "))
		}
		if entry.DiffStats != "" {
			fmt.Printf("- **Stats:** %s\n", entry.DiffStats)
		}

		if len(entry.Threads) > 0 {
			fmt.Print("\n### Associated Threads\n\n")
			for _, tRaw := range entry.Threads {
				t := tRaw.(map[string]interface{})
				fmt.Printf("**Thread:** %s (%v messages)\n\n", t["summary"], t["message_count"])
			}
		}
		fmt.Println()
	}

	if len(orphanThreads) > 0 {
		fmt.Print("## Research / Planning Threads (no associated commits)\n\n")
		for _, tRaw := range orphanThreads {
			t := tRaw.(map[string]interface{})
			fmt.Printf("- **%s** (%v messages, updated %s)\n", t["summary"], t["message_count"], t["updated_at"])
		}
	}
}

func handleGetSessionDir() {
	path, _ := getZedDBPath()
	if path != "" {
		path = filepath.Dir(path)
	}
	out := map[string]interface{}{
		"session_dir": path,
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func handleResolveSessionFile() {
	dbPath, _ := getZedDBPath()
	if dbPath == "" {
		dbPath = "zed-db"
	}
	out := map[string]interface{}{
		"session_file": dbPath,
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func installHookFile(hookPath, hookContent, marker string) error {
	existing, err := os.ReadFile(hookPath)
	if err == nil && strings.Contains(string(existing), marker) {
		return nil // Already installed
	}

	f, err := os.OpenFile(hookPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0755)
	if err != nil {
		return err
	}
	defer f.Close()

	if len(existing) == 0 {
		f.WriteString("#!/bin/sh\n")
	} else if !strings.HasSuffix(string(existing), "\n") {
		f.WriteString("\n")
	}
	f.WriteString(hookContent)
	os.Chmod(hookPath, 0755)
	return nil
}

func handleInstallHooks() {
	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	if repoRoot == "" {
		// Fall back to git repo root detection
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err == nil {
			repoRoot = strings.TrimSpace(string(out))
		}
	}
	count := 0
	if repoRoot != "" {
		marker := "Entire Zed Agent Lifecycle Hook"
		hooksDir := filepath.Join(repoRoot, ".git", "hooks")

		// Remove legacy pre-commit hook block if present
		preCommitPath := filepath.Join(hooksDir, "pre-commit")
		if existing, err := os.ReadFile(preCommitPath); err == nil && strings.Contains(string(existing), marker) {
			removeHookBlock(preCommitPath, string(existing), marker)
		}

		// Install post-commit hook
		// Lifecycle: on each commit, end the previous turn (capturing the transcript
		// delta since the last commit), then start a new turn for the next work unit.
		// On the very first commit, also initialize the session.
		postCommitPath := filepath.Join(hooksDir, "post-commit")
		postCommitContent := `
# --- Entire Zed Agent Lifecycle Hook ---
export PATH="$HOME/.local/bin:$HOME/bin:/usr/local/bin:$PATH"
ENTIRE_CMD="entire"
if command -v entire-patched >/dev/null 2>&1; then ENTIRE_CMD="entire-patched"; fi

# Check if a Zed session is already active
ENTIRE_STATUS=$($ENTIRE_CMD status 2>/dev/null)
if ! echo "$ENTIRE_STATUS" | grep -q "Zed ·"; then
    # First commit: initialize session, then start + end the first turn
    $ENTIRE_CMD hooks zed session-start >/dev/null 2>&1 || true
    $ENTIRE_CMD hooks zed turn-start >/dev/null 2>&1 || true
fi

# End the current turn (captures transcript from last commit to this one)
$ENTIRE_CMD hooks zed turn-end >/dev/null 2>&1 || true

# Start a new turn for the next unit of work
$ENTIRE_CMD hooks zed turn-start >/dev/null 2>&1 || true
# ---------------------------------------
`

		if err := installHookFile(postCommitPath, postCommitContent, marker); err == nil {
			// The hook registers 3 lifecycle events: session-start, turn-end, turn-start
			count = 3
		}
	}

	out := map[string]interface{}{
		"count": count,
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func removeHookBlock(hookPath, content, marker string) {
	startMarker := "# --- " + marker + " ---"
	endStr := "# ---------------------------------------\n"

	startIdx := strings.Index(content, "\n"+startMarker)
	if startIdx == -1 {
		startIdx = strings.Index(content, startMarker)
	}
	endIdx := strings.Index(content, endStr)
	if startIdx != -1 && endIdx != -1 {
		newContent := content[:startIdx] + content[endIdx+len(endStr):]
		os.WriteFile(hookPath, []byte(newContent), 0755)
	}
}

func handleUninstallHooks() {
	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	if repoRoot == "" {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err == nil {
			repoRoot = strings.TrimSpace(string(out))
		}
	}
	if repoRoot != "" {
		marker := "Entire Zed Agent Lifecycle Hook"
		hooksDir := filepath.Join(repoRoot, ".git", "hooks")

		// Remove from both pre-commit (legacy) and post-commit
		for _, hookFile := range []string{"pre-commit", "post-commit"} {
			hookPath := filepath.Join(hooksDir, hookFile)
			if existing, err := os.ReadFile(hookPath); err == nil {
				removeHookBlock(hookPath, string(existing), marker)
			}
		}
	}
	fmt.Println("{}")
}

func handleAreHooksInstalled() {
	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	if repoRoot == "" {
		out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err == nil {
			repoRoot = strings.TrimSpace(string(out))
		}
	}
	installed := false
	if repoRoot != "" {
		// Check post-commit (current) and pre-commit (legacy)
		for _, hookFile := range []string{"post-commit", "pre-commit"} {
			hookPath := filepath.Join(repoRoot, ".git", "hooks", hookFile)
			existing, err := os.ReadFile(hookPath)
			if err == nil && strings.Contains(string(existing), "Entire Zed Agent Lifecycle Hook") {
				installed = true
				break
			}
		}
	}

	out := map[string]interface{}{
		"installed": installed,
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func handleCalculateTokens() {
	dbPath, err := getZedDBPath()
	if err != nil {
		fmt.Println("{}")
		return
	}

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro") // Open read-only
	if err != nil {
		fmt.Println("{}")
		return
	}
	defer db.Close()

	_, rawData, err := fetchLatestThread(db)
	if err != nil {
		fmt.Println("{}")
		return
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(rawData, &parsed); err != nil {
		fmt.Println("{}")
		return
	}

	var inputTokens, outputTokens, cacheReadTokens, apiCallCount int

	if usage, ok := parsed["request_token_usage"].(map[string]interface{}); ok {
		for _, reqRaw := range usage {
			if req, ok := reqRaw.(map[string]interface{}); ok {
				apiCallCount++
				if in, ok := req["input_tokens"].(float64); ok {
					inputTokens += int(in)
				}
				if out, ok := req["output_tokens"].(float64); ok {
					outputTokens += int(out)
				}
				if cacheRead, ok := req["cache_read_input_tokens"].(float64); ok {
					cacheReadTokens += int(cacheRead)
				}
			}
		}
	}

	out := map[string]interface{}{
		"input_tokens":          inputTokens,
		"cache_creation_tokens": 0,
		"cache_read_tokens":     cacheReadTokens,
		"output_tokens":         outputTokens,
		"api_call_count":        apiCallCount,
	}
	json.NewEncoder(os.Stdout).Encode(out)
}
