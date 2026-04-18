package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
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

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: entire-agent-zed-parser <subcommand>")
		os.Exit(1)
	}

	subcommand := os.Args[1]

	switch subcommand {
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
	default:
		// Required by the Entire External Agent Protocol: Exit gracefully if unsupported
		fmt.Println("{}")
		os.Exit(0)
	}
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

	// Standard Linux path. Will need to expand for macOS in future revisions.
	path := filepath.Join(usr.HomeDir, ".local", "share", "zed", "threads", "threads.db")

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("database not found at %s", path)
	}

	return path, nil
}

func handleStart() {
	// The protocol expects a successful exit for start
	fmt.Println("{}")
}

func handleStop() {
	// The protocol expects a successful exit for stop
	fmt.Println("{}")
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

	sessionID := "zed-current"
	if sid, ok := payload["session_id"].(string); ok && sid != "" {
		sessionID = sid
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
		out["prompt"] = "Zed AI prompt"
	}

	json.NewEncoder(os.Stdout).Encode(out)
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

func handleInstallHooks() {
	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	count := 0
	if repoRoot != "" {
		hookPath := filepath.Join(repoRoot, ".git", "hooks", "pre-commit")
		hookContent := `
# --- Entire Zed Agent Lifecycle Hook ---
export PATH="$HOME/.local/bin:$HOME/bin:/usr/local/bin:$PATH"
ENTIRE_CMD="entire"
if command -v entire-patched >/dev/null 2>&1; then ENTIRE_CMD="entire-patched"; fi

ENTIRE_STATUS=$($ENTIRE_CMD status 2>/dev/null)
if ! echo "$ENTIRE_STATUS" | grep -q "Zed ·"; then
    $ENTIRE_CMD hooks zed session-start >/dev/null 2>&1 || true
    $ENTIRE_CMD hooks zed turn-start >/dev/null 2>&1 || true
fi
$ENTIRE_CMD hooks zed turn-end >/dev/null 2>&1 || true
# ---------------------------------------
`
		existing, err := os.ReadFile(hookPath)
		if err != nil || !strings.Contains(string(existing), "Entire Zed Agent Lifecycle Hook") {
			f, err := os.OpenFile(hookPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0755)
			if err == nil {
				if len(existing) == 0 {
					f.WriteString("#!/bin/sh\n")
				} else if !strings.HasSuffix(string(existing), "\n") {
					f.WriteString("\n")
				}
				f.WriteString(hookContent)
				f.Close()
				os.Chmod(hookPath, 0755)
				count = 1
			}
		} else {
			count = 1 // Already installed
		}
	}

	out := map[string]interface{}{
		"count": count,
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func handleUninstallHooks() {
	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	if repoRoot != "" {
		hookPath := filepath.Join(repoRoot, ".git", "hooks", "pre-commit")
		existing, err := os.ReadFile(hookPath)
		if err == nil {
			content := string(existing)
			startIdx := strings.Index(content, "\n# --- Entire Zed Agent Lifecycle Hook ---")
			if startIdx == -1 {
				startIdx = strings.Index(content, "# --- Entire Zed Agent Lifecycle Hook ---")
			}
			endStr := "# ---------------------------------------\n"
			endIdx := strings.Index(content, endStr)
			if startIdx != -1 && endIdx != -1 {
				newContent := content[:startIdx] + content[endIdx+len(endStr):]
				os.WriteFile(hookPath, []byte(newContent), 0755)
			}
		}
	}
	fmt.Println("{}")
}

func handleAreHooksInstalled() {
	repoRoot := os.Getenv("ENTIRE_REPO_ROOT")
	installed := false
	if repoRoot != "" {
		hookPath := filepath.Join(repoRoot, ".git", "hooks", "pre-commit")
		existing, err := os.ReadFile(hookPath)
		if err == nil && strings.Contains(string(existing), "Entire Zed Agent Lifecycle Hook") {
			installed = true
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
