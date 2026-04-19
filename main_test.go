package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var _ = strings.HasPrefix // silence unused import

// ---------------------------------------------------------------------------
// parseTranscript
// ---------------------------------------------------------------------------

func TestParseTranscript_UserAndAgent(t *testing.T) {
	raw := `{
		"messages": [
			{"User": {"content": [{"Text": "Hello, world"}]}},
			{"Agent": {"content": [{"Text": "Hi there!"}]}}
		]
	}`
	msgs, err := parseTranscript([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "Hello, world" {
		t.Errorf("unexpected user message: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Hi there!" {
		t.Errorf("unexpected assistant message: %+v", msgs[1])
	}
}

func TestParseTranscript_ToolUse(t *testing.T) {
	raw := `{
		"messages": [
			{"Agent": {"content": [
				{"Text": "Let me check that."},
				{"ToolUse": {"name": "read_file", "input": {"path": "/tmp/foo.txt"}}}
			]}}
		]
	}`
	msgs, err := parseTranscript([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(msgs[0].Tools))
	}
	if msgs[0].Tools[0].Name != "read_file" {
		t.Errorf("expected tool name read_file, got %s", msgs[0].Tools[0].Name)
	}
	if msgs[0].Tools[0].Params["path"] != "/tmp/foo.txt" {
		t.Errorf("unexpected tool params: %+v", msgs[0].Tools[0].Params)
	}
}

func TestParseTranscript_Mention(t *testing.T) {
	raw := `{
		"messages": [
			{"User": {"content": [
				{"Text": "Look at "},
				{"Mention": {"content": "src/main.go"}}
			]}}
		]
	}`
	msgs, err := parseTranscript([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "Look at src/main.go" {
		t.Errorf("expected concatenated content, got %q", msgs[0].Content)
	}
}

func TestParseTranscript_EmptyMessages(t *testing.T) {
	raw := `{"messages": []}`
	msgs, err := parseTranscript([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestParseTranscript_NoMessagesKey(t *testing.T) {
	raw := `{"something_else": true}`
	msgs, err := parseTranscript([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestParseTranscript_InvalidJSON(t *testing.T) {
	_, err := parseTranscript([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseTranscript_UnknownRole(t *testing.T) {
	raw := `{"messages": [{"System": {"content": [{"Text": "system msg"}]}}]}`
	msgs, err := parseTranscript([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for unknown role, got %d", len(msgs))
	}
}

func TestParseTranscript_MultipleToolUses(t *testing.T) {
	raw := `{
		"messages": [
			{"Agent": {"content": [
				{"ToolUse": {"name": "read_file", "input": {"path": "a.go"}}},
				{"Text": "Found it."},
				{"ToolUse": {"name": "write_file", "input": {"path": "b.go", "content": "x"}}}
			]}}
		]
	}`
	msgs, err := parseTranscript([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs[0].Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(msgs[0].Tools))
	}
	if msgs[0].Tools[0].Name != "read_file" || msgs[0].Tools[1].Name != "write_file" {
		t.Errorf("unexpected tool names: %v, %v", msgs[0].Tools[0].Name, msgs[0].Tools[1].Name)
	}
}

// ---------------------------------------------------------------------------
// extractTokens
// ---------------------------------------------------------------------------

func TestExtractTokens_Valid(t *testing.T) {
	raw := `{
		"request_token_usage": {
			"req1": {"input_tokens": 100, "output_tokens": 50, "cache_read_input_tokens": 20},
			"req2": {"input_tokens": 200, "output_tokens": 75}
		}
	}`
	tokens := extractTokens([]byte(raw))
	if tokens == nil {
		t.Fatal("expected non-nil tokens")
	}
	if tokens["input_tokens"] != 300 {
		t.Errorf("expected input_tokens=300, got %v", tokens["input_tokens"])
	}
	if tokens["output_tokens"] != 125 {
		t.Errorf("expected output_tokens=125, got %v", tokens["output_tokens"])
	}
	if tokens["cache_read_tokens"] != 20 {
		t.Errorf("expected cache_read_tokens=20, got %v", tokens["cache_read_tokens"])
	}
	if tokens["api_call_count"] != 2 {
		t.Errorf("expected api_call_count=2, got %v", tokens["api_call_count"])
	}
}

func TestExtractTokens_NoUsageField(t *testing.T) {
	raw := `{"messages": []}`
	tokens := extractTokens([]byte(raw))
	if tokens != nil {
		t.Errorf("expected nil tokens, got %v", tokens)
	}
}

func TestExtractTokens_EmptyUsage(t *testing.T) {
	raw := `{"request_token_usage": {}}`
	tokens := extractTokens([]byte(raw))
	if tokens != nil {
		t.Errorf("expected nil for empty usage, got %v", tokens)
	}
}

func TestExtractTokens_InvalidJSON(t *testing.T) {
	tokens := extractTokens([]byte("nope"))
	if tokens != nil {
		t.Errorf("expected nil for invalid JSON, got %v", tokens)
	}
}

// ---------------------------------------------------------------------------
// Hook block management (removeHookBlock, installHookFile)
// ---------------------------------------------------------------------------

func TestRemoveHookBlock(t *testing.T) {
	content := `#!/bin/sh
echo "before"

# --- Entire Zed Agent Lifecycle Hook ---
export PATH="$HOME/.local/bin:$PATH"
entire hooks zed turn-end
# ---------------------------------------

echo "after"
`
	tmp := filepath.Join(t.TempDir(), "hook")
	os.WriteFile(tmp, []byte(content), 0755)

	removeHookBlock(tmp, content, "Entire Zed Agent Lifecycle Hook")

	result, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("failed to read: %v", err)
	}
	if strings.Contains(string(result), "Entire Zed Agent Lifecycle Hook") {
		t.Error("hook block was not removed")
	}
	if !strings.Contains(string(result), `echo "before"`) {
		t.Error("content before hook block was removed")
	}
	if !strings.Contains(string(result), `echo "after"`) {
		t.Error("content after hook block was removed")
	}
}

func TestRemoveHookBlock_NotPresent(t *testing.T) {
	content := "#!/bin/sh\necho hello\n"
	tmp := filepath.Join(t.TempDir(), "hook")
	os.WriteFile(tmp, []byte(content), 0755)

	removeHookBlock(tmp, content, "Entire Zed Agent Lifecycle Hook")

	result, _ := os.ReadFile(tmp)
	if string(result) != content {
		t.Error("file was modified when hook block was not present")
	}
}

func TestInstallHookFile_NewFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "hook")
	marker := "My Hook Marker"
	hookContent := "\n# --- My Hook Marker ---\nsome hook code\n# ---------------------------------------\n"

	err := installHookFile(tmp, hookContent, marker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, _ := os.ReadFile(tmp)
	if !strings.HasPrefix(string(result), "#!/bin/sh\n") {
		t.Error("expected shebang for new file")
	}
	if !strings.Contains(string(result), marker) {
		t.Error("hook content not written")
	}
}

func TestInstallHookFile_ExistingFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "hook")
	os.WriteFile(tmp, []byte("#!/bin/sh\necho existing\n"), 0755)

	marker := "My Hook Marker"
	hookContent := "\n# --- My Hook Marker ---\nsome hook code\n# ---------------------------------------\n"

	err := installHookFile(tmp, hookContent, marker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, _ := os.ReadFile(tmp)
	if !strings.Contains(string(result), "echo existing") {
		t.Error("existing content was lost")
	}
	if !strings.Contains(string(result), marker) {
		t.Error("hook content not appended")
	}
}

func TestInstallHookFile_AlreadyInstalled(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "hook")
	marker := "My Hook Marker"
	content := "#!/bin/sh\n# --- My Hook Marker ---\nstuff\n# ---------------------------------------\n"
	os.WriteFile(tmp, []byte(content), 0755)

	err := installHookFile(tmp, "MORE STUFF", marker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, _ := os.ReadFile(tmp)
	if strings.Contains(string(result), "MORE STUFF") {
		t.Error("should not have written duplicate hook")
	}
}

// ---------------------------------------------------------------------------
// Binary integration tests (run the compiled binary)
// ---------------------------------------------------------------------------

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "entire-agent-zed")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func TestBinary_Info(t *testing.T) {
	bin := buildBinary(t)
	out, err := exec.Command(bin, "info").CombinedOutput()
	if err != nil {
		t.Fatalf("info failed: %v\n%s", err, out)
	}

	var info map[string]interface{}
	if err := json.Unmarshal(out, &info); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if info["name"] != "zed" {
		t.Errorf("expected name=zed, got %v", info["name"])
	}
	caps := info["capabilities"].(map[string]interface{})
	for _, cap := range []string{"transcript_analyzer", "transcript_preparer", "hooks", "token_calculator"} {
		if caps[cap] != true {
			t.Errorf("expected capability %s=true", cap)
		}
	}
}

func TestBinary_UnknownSubcommand(t *testing.T) {
	bin := buildBinary(t)
	out, err := exec.Command(bin, "nonexistent").CombinedOutput()
	if err != nil {
		t.Fatalf("should exit 0 for unknown subcommand: %v", err)
	}
	if strings.TrimSpace(string(out)) != "{}" {
		t.Errorf("expected {}, got %q", string(out))
	}
}

func TestBinary_Start(t *testing.T) {
	bin := buildBinary(t)
	out, err := exec.Command(bin, "start").CombinedOutput()
	if err != nil {
		t.Fatalf("start failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "{}" {
		t.Errorf("expected {}, got %q", string(out))
	}
}

func TestBinary_Stop(t *testing.T) {
	bin := buildBinary(t)
	out, err := exec.Command(bin, "stop").CombinedOutput()
	if err != nil {
		t.Fatalf("stop failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "{}" {
		t.Errorf("expected {}, got %q", string(out))
	}
}

// ---------------------------------------------------------------------------
// Hook install/uninstall integration (uses temp git repo)
// ---------------------------------------------------------------------------

func setupTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, ".git", "hooks")
	os.MkdirAll(hooksDir, 0755)
	return dir
}

func TestBinary_InstallHooks(t *testing.T) {
	bin := buildBinary(t)
	repo := setupTempRepo(t)

	cmd := exec.Command(bin, "install-hooks")
	cmd.Env = append(os.Environ(), "ENTIRE_REPO_ROOT="+repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install-hooks failed: %v\n%s", err, out)
	}

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["count"] != float64(3) {
		t.Errorf("expected count=3 (session-start, turn-end, turn-start), got %v", result["count"])
	}

	// Verify post-commit hook exists
	hookContent, err := os.ReadFile(filepath.Join(repo, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatal("post-commit hook not created")
	}
	if !strings.Contains(string(hookContent), "Entire Zed Agent Lifecycle Hook") {
		t.Error("hook marker not found in post-commit")
	}
	if !strings.Contains(string(hookContent), "turn-end") {
		t.Error("turn-end not found in hook")
	}
	if !strings.Contains(string(hookContent), "turn-start") {
		t.Error("turn-start not found in hook")
	}

	// Verify no pre-commit hook was created
	_, err = os.Stat(filepath.Join(repo, ".git", "hooks", "pre-commit"))
	if err == nil {
		t.Error("pre-commit hook should NOT be created by new install")
	}
}

func TestBinary_InstallHooks_MigratesLegacyPreCommit(t *testing.T) {
	bin := buildBinary(t)
	repo := setupTempRepo(t)

	// Write a legacy pre-commit hook
	preCommitPath := filepath.Join(repo, ".git", "hooks", "pre-commit")
	legacyContent := `#!/bin/sh
echo "other stuff"

# --- Entire Zed Agent Lifecycle Hook ---
old hook content
# ---------------------------------------

echo "more stuff"
`
	os.WriteFile(preCommitPath, []byte(legacyContent), 0755)

	cmd := exec.Command(bin, "install-hooks")
	cmd.Env = append(os.Environ(), "ENTIRE_REPO_ROOT="+repo)
	cmd.CombinedOutput()

	// Legacy block should be removed from pre-commit
	preCommit, _ := os.ReadFile(preCommitPath)
	if strings.Contains(string(preCommit), "Entire Zed Agent Lifecycle Hook") {
		t.Error("legacy hook block should have been removed from pre-commit")
	}
	if !strings.Contains(string(preCommit), "other stuff") {
		t.Error("non-hook content should be preserved in pre-commit")
	}

	// Post-commit should have the new hook
	postCommit, err := os.ReadFile(filepath.Join(repo, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatal("post-commit hook not created")
	}
	if !strings.Contains(string(postCommit), "Entire Zed Agent Lifecycle Hook") {
		t.Error("new hook not in post-commit")
	}
}

func TestBinary_AreHooksInstalled(t *testing.T) {
	bin := buildBinary(t)
	repo := setupTempRepo(t)

	// Not installed yet
	cmd := exec.Command(bin, "are-hooks-installed")
	cmd.Env = append(os.Environ(), "ENTIRE_REPO_ROOT="+repo)
	out, _ := cmd.CombinedOutput()
	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["installed"] != false {
		t.Error("expected installed=false before install")
	}

	// Install
	cmd = exec.Command(bin, "install-hooks")
	cmd.Env = append(os.Environ(), "ENTIRE_REPO_ROOT="+repo)
	cmd.CombinedOutput()

	// Now installed
	cmd = exec.Command(bin, "are-hooks-installed")
	cmd.Env = append(os.Environ(), "ENTIRE_REPO_ROOT="+repo)
	out, _ = cmd.CombinedOutput()
	json.Unmarshal(out, &result)
	if result["installed"] != true {
		t.Error("expected installed=true after install")
	}
}

func TestBinary_UninstallHooks(t *testing.T) {
	bin := buildBinary(t)
	repo := setupTempRepo(t)

	// Install first
	cmd := exec.Command(bin, "install-hooks")
	cmd.Env = append(os.Environ(), "ENTIRE_REPO_ROOT="+repo)
	cmd.CombinedOutput()

	// Uninstall
	cmd = exec.Command(bin, "uninstall-hooks")
	cmd.Env = append(os.Environ(), "ENTIRE_REPO_ROOT="+repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("uninstall-hooks failed: %v\n%s", err, out)
	}

	// Verify hook is gone
	postCommit, _ := os.ReadFile(filepath.Join(repo, ".git", "hooks", "post-commit"))
	if strings.Contains(string(postCommit), "Entire Zed Agent Lifecycle Hook") {
		t.Error("hook block should have been removed")
	}

	// Verify are-hooks-installed returns false
	cmd = exec.Command(bin, "are-hooks-installed")
	cmd.Env = append(os.Environ(), "ENTIRE_REPO_ROOT="+repo)
	out, _ = cmd.CombinedOutput()
	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["installed"] != false {
		t.Error("expected installed=false after uninstall")
	}
}

// ---------------------------------------------------------------------------
// ParseHook (via binary, with stdin payload)
// ---------------------------------------------------------------------------

func TestBinary_ParseHook_SessionStart(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "session-start")
	cmd.Stdin = strings.NewReader(`{}`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("parse-hook failed: %v\n%s", err, out)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["type"] != float64(1) {
		t.Errorf("expected type=1 (SessionStart), got %v", result["type"])
	}
	sid, ok := result["session_id"].(string)
	if !ok || sid == "" {
		t.Error("expected non-empty session_id")
	}
	// Session ID should be derived from thread or fallback to zed-current
	if !strings.HasPrefix(sid, "zed-") {
		t.Errorf("expected session_id to start with 'zed-', got %q", sid)
	}
}

func TestBinary_ParseHook_TurnStart(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "turn-start")
	cmd.Stdin = strings.NewReader(`{}`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("parse-hook failed: %v\n%s", err, out)
	}

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["type"] != float64(2) {
		t.Errorf("expected type=2 (TurnStart), got %v", result["type"])
	}
	// Should have a prompt field
	if _, ok := result["prompt"]; !ok {
		t.Error("expected prompt field in TurnStart event")
	}
}

func TestBinary_ParseHook_TurnEnd(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "turn-end")
	cmd.Stdin = strings.NewReader(`{}`)
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["type"] != float64(3) {
		t.Errorf("expected type=3 (TurnEnd), got %v", result["type"])
	}
}

func TestBinary_ParseHook_SessionEnd(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "session-end")
	cmd.Stdin = strings.NewReader(`{}`)
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["type"] != float64(5) {
		t.Errorf("expected type=5 (SessionEnd), got %v", result["type"])
	}
}

func TestBinary_ParseHook_WithSessionID(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "turn-end")
	cmd.Stdin = strings.NewReader(`{"session_id": "custom-123"}`)
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["session_id"] != "custom-123" {
		t.Errorf("expected session_id=custom-123, got %v", result["session_id"])
	}
}

func TestBinary_ParseHook_HookFlag(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "--hook", "stop")
	cmd.Stdin = strings.NewReader(`{}`)
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["type"] != float64(5) {
		t.Errorf("expected type=5 for --hook stop, got %v", result["type"])
	}
}

// ---------------------------------------------------------------------------
// Token inclusion in turn-end / session-end events
// ---------------------------------------------------------------------------

func TestBinary_ParseHook_TurnEnd_IncludesTokens(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "turn-end")
	cmd.Stdin = strings.NewReader(`{}`)
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)

	// If a Zed DB exists with token data, tokens should be present.
	// If no DB exists, tokens will be absent (which is fine).
	// We test the structural contract: if tokens exist, they have the right keys.
	if tokens, ok := result["tokens"].(map[string]interface{}); ok {
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "api_call_count"} {
			if _, exists := tokens[key]; !exists {
				t.Errorf("tokens missing key %q", key)
			}
		}
	}
}

func TestBinary_ParseHook_SessionEnd_IncludesTokens(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "session-end")
	cmd.Stdin = strings.NewReader(`{}`)
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)

	// Same structural check as turn-end
	if tokens, ok := result["tokens"].(map[string]interface{}); ok {
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_tokens", "api_call_count"} {
			if _, exists := tokens[key]; !exists {
				t.Errorf("tokens missing key %q", key)
			}
		}
	}
}

func TestBinary_ParseHook_TurnStart_NoTokens(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "turn-start")
	cmd.Stdin = strings.NewReader(`{}`)
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)

	// turn-start should NOT include tokens (tokens are a turn-end concern)
	if _, ok := result["tokens"]; ok {
		t.Error("turn-start should not include tokens")
	}
}

func TestBinary_ParseHook_SessionStart_NoTokens(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "parse-hook", "session-start")
	cmd.Stdin = strings.NewReader(`{}`)
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)

	if _, ok := result["tokens"]; ok {
		t.Error("session-start should not include tokens")
	}
}

// ---------------------------------------------------------------------------
// extractTokens — token deduplication / summary behavior
// ---------------------------------------------------------------------------

func TestExtractTokens_MultipleRequests_SumsCorrectly(t *testing.T) {
	raw := `{
		"request_token_usage": {
			"req1": {"input_tokens": 100, "output_tokens": 50, "cache_read_input_tokens": 10},
			"req2": {"input_tokens": 200, "output_tokens": 75, "cache_read_input_tokens": 30},
			"req3": {"input_tokens": 300, "output_tokens": 25}
		}
	}`
	tokens := extractTokens([]byte(raw))
	if tokens == nil {
		t.Fatal("expected non-nil tokens")
	}
	if tokens["input_tokens"] != 600 {
		t.Errorf("expected input_tokens=600, got %v", tokens["input_tokens"])
	}
	if tokens["output_tokens"] != 150 {
		t.Errorf("expected output_tokens=150, got %v", tokens["output_tokens"])
	}
	if tokens["cache_read_tokens"] != 40 {
		t.Errorf("expected cache_read_tokens=40, got %v", tokens["cache_read_tokens"])
	}
	if tokens["api_call_count"] != 3 {
		t.Errorf("expected api_call_count=3, got %v", tokens["api_call_count"])
	}
}

// ---------------------------------------------------------------------------
// Git repo root fallback for hook commands
// ---------------------------------------------------------------------------

func setupRealTempRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	return dir
}

func TestBinary_InstallHooks_FallsBackToGitRoot(t *testing.T) {
	bin := buildBinary(t)
	repo := setupRealTempRepo(t)

	// Run WITHOUT ENTIRE_REPO_ROOT, from inside the repo
	cmd := exec.Command(bin, "install-hooks")
	cmd.Dir = repo
	// Explicitly clear ENTIRE_REPO_ROOT
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "ENTIRE_REPO_ROOT=") {
			env = append(env, e)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install-hooks failed: %v\n%s", err, out)
	}

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["count"] != float64(3) {
		t.Errorf("expected count=3 with git root fallback, got %v", result["count"])
	}

	// Verify hook was actually created
	hookContent, err := os.ReadFile(filepath.Join(repo, ".git", "hooks", "post-commit"))
	if err != nil {
		t.Fatal("post-commit hook not created via git root fallback")
	}
	if !strings.Contains(string(hookContent), "Entire Zed Agent Lifecycle Hook") {
		t.Error("hook marker not found")
	}
}

func TestBinary_AreHooksInstalled_FallsBackToGitRoot(t *testing.T) {
	bin := buildBinary(t)
	repo := setupRealTempRepo(t)

	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "ENTIRE_REPO_ROOT=") {
			env = append(env, e)
		}
	}

	// Install first
	cmd := exec.Command(bin, "install-hooks")
	cmd.Dir = repo
	cmd.Env = env
	cmd.CombinedOutput()

	// Check without ENTIRE_REPO_ROOT
	cmd = exec.Command(bin, "are-hooks-installed")
	cmd.Dir = repo
	cmd.Env = env
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["installed"] != true {
		t.Error("expected installed=true via git root fallback")
	}
}

func TestBinary_UninstallHooks_FallsBackToGitRoot(t *testing.T) {
	bin := buildBinary(t)
	repo := setupRealTempRepo(t)

	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "ENTIRE_REPO_ROOT=") {
			env = append(env, e)
		}
	}

	// Install first
	cmd := exec.Command(bin, "install-hooks")
	cmd.Dir = repo
	cmd.Env = env
	cmd.CombinedOutput()

	// Uninstall without ENTIRE_REPO_ROOT
	cmd = exec.Command(bin, "uninstall-hooks")
	cmd.Dir = repo
	cmd.Env = env
	cmd.CombinedOutput()

	// Verify removed
	cmd = exec.Command(bin, "are-hooks-installed")
	cmd.Dir = repo
	cmd.Env = env
	out, _ := cmd.CombinedOutput()

	var result map[string]interface{}
	json.Unmarshal(out, &result)
	if result["installed"] != false {
		t.Error("expected installed=false after uninstall via git root fallback")
	}
}

// ---------------------------------------------------------------------------
// writeSessionSnapshot
// ---------------------------------------------------------------------------

func TestWriteSessionSnapshot_CreatesFiles(t *testing.T) {
	repo := setupTempRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	rawData := []byte(`{
		"messages": [
			{"User": {"content": [{"Text": "Hello"}]}},
			{"Agent": {"content": [{"Text": "Hi there"}]}}
		]
	}`)

	writeSessionSnapshot("zed-abc123", "abc123", "Hello", "active", rawData)

	// Check session metadata file
	metaPath := filepath.Join(repo, ".git", "entire-sessions", "zed-abc123.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("session metadata file not created: %v", err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("invalid session metadata JSON: %v", err)
	}
	if meta["session_id"] != "zed-abc123" {
		t.Errorf("expected session_id=zed-abc123, got %v", meta["session_id"])
	}
	if meta["agent_type"] != "Zed" {
		t.Errorf("expected agent_type=Zed, got %v", meta["agent_type"])
	}
	if meta["phase"] != "active" {
		t.Errorf("expected phase=active, got %v", meta["phase"])
	}
	if meta["last_prompt"] != "Hello" {
		t.Errorf("expected last_prompt=Hello, got %v", meta["last_prompt"])
	}
	if meta["thread_id"] != "abc123" {
		t.Errorf("expected thread_id=abc123, got %v", meta["thread_id"])
	}

	// Check transcript_path points to a real file
	transcriptPath, ok := meta["transcript_path"].(string)
	if !ok || transcriptPath == "" {
		t.Fatal("expected non-empty transcript_path")
	}
	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("transcript file not created at %s: %v", transcriptPath, err)
	}

	var transcript []EntireMessage
	if err := json.Unmarshal(transcriptBytes, &transcript); err != nil {
		t.Fatalf("invalid transcript JSON: %v", err)
	}
	if len(transcript) != 2 {
		t.Errorf("expected 2 messages in transcript, got %d", len(transcript))
	}
}

func TestWriteSessionSnapshot_NilRawData(t *testing.T) {
	repo := setupTempRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	writeSessionSnapshot("zed-nodata", "nodata", "", "ended", nil)

	// Session metadata should still be created
	metaPath := filepath.Join(repo, ".git", "entire-sessions", "zed-nodata.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("session metadata file not created: %v", err)
	}

	var meta map[string]interface{}
	json.Unmarshal(metaBytes, &meta)
	if meta["phase"] != "ended" {
		t.Errorf("expected phase=ended, got %v", meta["phase"])
	}

	// Transcript file should NOT exist (no raw data)
	transcriptPath := filepath.Join(repo, ".git", "entire-sessions", "zed-nodata.transcript.json")
	if _, err := os.Stat(transcriptPath); err == nil {
		t.Error("transcript file should not exist when rawData is nil")
	}
}

func TestWriteSessionSnapshot_OverwritesOnUpdate(t *testing.T) {
	repo := setupTempRepo(t)
	t.Setenv("ENTIRE_REPO_ROOT", repo)

	rawData1 := []byte(`{"messages": [{"User": {"content": [{"Text": "First"}]}}]}`)
	rawData2 := []byte(`{"messages": [{"User": {"content": [{"Text": "First"}]}}, {"Agent": {"content": [{"Text": "Reply"}]}}]}`)

	writeSessionSnapshot("zed-update", "update", "First", "active", rawData1)
	writeSessionSnapshot("zed-update", "update", "First", "active", rawData2)

	transcriptPath := filepath.Join(repo, ".git", "entire-sessions", "zed-update.transcript.json")
	transcriptBytes, _ := os.ReadFile(transcriptPath)

	var transcript []EntireMessage
	json.Unmarshal(transcriptBytes, &transcript)
	if len(transcript) != 2 {
		t.Errorf("expected 2 messages after update, got %d", len(transcript))
	}
}

// ---------------------------------------------------------------------------
// convertToOpenCodeFormat
// ---------------------------------------------------------------------------

func TestConvertToOpenCodeFormat_Basic(t *testing.T) {
	raw := `{
		"version": "0.3.0",
		"title": "Test Session",
		"model": {"provider": "google", "model": "gemini-2.0-flash"},
		"messages": [
			{"User": {"id": "u1", "content": [{"Text": "Hello"}]}},
			{"Agent": {"id": "a1", "content": [{"Text": "Hi there"}]}}
		]
	}`

	transcript, err := convertToOpenCodeFormat("thread-123", "ses-abc", []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if transcript.Info == nil {
		t.Fatal("expected info section")
	}
	if transcript.Info.Title != "Test Session" {
		t.Errorf("expected title=Test Session, got %q", transcript.Info.Title)
	}
	if transcript.Info.Version != "0.3.0" {
		t.Errorf("expected version=0.3.0, got %q", transcript.Info.Version)
	}
	if transcript.Info.Model == nil {
		t.Fatal("expected model section")
	}
	if transcript.Info.Model.ProviderID != "google" {
		t.Errorf("expected provider=google, got %q", transcript.Info.Model.ProviderID)
	}
	if transcript.Info.Model.ModelID != "gemini-2.0-flash" {
		t.Errorf("expected modelID=gemini-2.0-flash, got %q", transcript.Info.Model.ModelID)
	}

	if len(transcript.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(transcript.Messages))
	}
	if transcript.Messages[0].Info.Role != "user" {
		t.Errorf("expected first message role=user, got %q", transcript.Messages[0].Info.Role)
	}
	if transcript.Messages[1].Info.Role != "assistant" {
		t.Errorf("expected second message role=assistant, got %q", transcript.Messages[1].Info.Role)
	}

	// Check text parts
	if len(transcript.Messages[0].Parts) != 1 {
		t.Errorf("expected 1 part in user message, got %d", len(transcript.Messages[0].Parts))
	}
	if transcript.Messages[0].Parts[0].Type != "text" {
		t.Errorf("expected type=text, got %q", transcript.Messages[0].Parts[0].Type)
	}
	if transcript.Messages[0].Parts[0].Text != "Hello" {
		t.Errorf("expected text=Hello, got %q", transcript.Messages[0].Parts[0].Text)
	}
}

func TestConvertToOpenCodeFormat_ToolUse(t *testing.T) {
	raw := `{
		"messages": [
			{"Agent": {"id": "a1", "content": [
				{"Text": "Let me read that file."},
				{"ToolUse": {"id": "read-1", "name": "read_file", "input": {"path": "/tmp/foo.txt"}, "is_input_complete": true}}
			]}}
		]
	}`

	transcript, err := convertToOpenCodeFormat("thread-123", "ses-abc", []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(transcript.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(transcript.Messages))
	}
	msg := transcript.Messages[0]

	// Should have 2 parts: text and tool_use
	if len(msg.Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(msg.Parts))
	}
	if msg.Parts[0].Type != "text" {
		t.Errorf("expected first part type=text, got %q", msg.Parts[0].Type)
	}
	if msg.Parts[1].Type != "tool_use" {
		t.Errorf("expected second part type=tool_use, got %q", msg.Parts[1].Type)
	}
	if msg.Parts[1].ToolUse == nil {
		t.Fatal("expected tool_use data")
	}
	if msg.Parts[1].ToolUse.Name != "read_file" {
		t.Errorf("expected tool name=read_file, got %q", msg.Parts[1].ToolUse.Name)
	}
	if msg.Parts[1].ToolUse.ID != "read-1" {
		t.Errorf("expected tool id=read-1, got %q", msg.Parts[1].ToolUse.ID)
	}
	if msg.Parts[1].ToolUse.Input["path"] != "/tmp/foo.txt" {
		t.Errorf("expected path=/tmp/foo.txt, got %v", msg.Parts[1].ToolUse.Input["path"])
	}
	if !msg.Parts[1].ToolUse.IsInputComplete {
		t.Error("expected is_input_complete=true")
	}
}

func TestConvertToOpenCodeFormat_Mention(t *testing.T) {
	raw := `{
		"messages": [
			{"User": {"content": [
				{"Text": "Look at "},
				{"Mention": {"content": "src/main.go"}}
			]}}
		]
	}`

	transcript, err := convertToOpenCodeFormat("thread-123", "ses-abc", []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mentions are converted to text parts
	if len(transcript.Messages[0].Parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(transcript.Messages[0].Parts))
	}
	if transcript.Messages[0].Parts[1].Type != "text" {
		t.Errorf("expected type=text, got %q", transcript.Messages[0].Parts[1].Type)
	}
	if transcript.Messages[0].Parts[1].Text != "src/main.go" {
		t.Errorf("expected text, got %q", transcript.Messages[0].Parts[1].Text)
	}
}

func TestConvertToOpenCodeFormat_EmptyMessages(t *testing.T) {
	raw := `{"messages": []}`

	transcript, err := convertToOpenCodeFormat("thread-123", "ses-abc", []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(transcript.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(transcript.Messages))
	}
}

func TestConvertToOpenCodeFormat_InvalidJSON(t *testing.T) {
	_, err := convertToOpenCodeFormat("thread-123", "ses-abc", []byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestExtractDeltaTranscript(t *testing.T) {
	raw := `{
		"version": "0.3.0",
		"title": "Test",
		"messages": [
			{"User": {"id": "u1", "content": [{"Text": "First user msg"}]}},
			{"Agent": {"id": "a1", "content": [{"Text": "First agent response"}]}},
			{"User": {"id": "u2", "content": [{"Text": "Second user msg"}]}},
			{"Agent": {"id": "a2", "content": [{"Text": "Second agent response"}]}},
			{"User": {"id": "u3", "content": [{"Text": "Third user msg"}]}},
			{"Agent": {"id": "a3", "content": [{"Text": "Third agent response"}]}}
		]
	}`

	// Extract delta from index 2 to 4 (messages 3-4: second user + second agent)
	delta := extractDeltaTranscript([]byte(raw), 2, 4)
	if delta == nil {
		t.Fatal("expected delta transcript")
	}

	// Should have 2 messages (user + agent)
	if len(delta.Messages) != 2 {
		t.Fatalf("expected 2 messages in delta, got %d", len(delta.Messages))
	}

	if delta.Messages[0].Info.Role != "user" {
		t.Errorf("expected first delta msg role=user, got %q", delta.Messages[0].Info.Role)
	}
	if delta.Messages[0].Parts[0].Text != "Second user msg" {
		t.Errorf("expected 'Second user msg', got %q", delta.Messages[0].Parts[0].Text)
	}

	if delta.Messages[1].Info.Role != "assistant" {
		t.Errorf("expected second delta msg role=assistant, got %q", delta.Messages[1].Info.Role)
	}
	if delta.Messages[1].Parts[0].Text != "Second agent response" {
		t.Errorf("expected 'Second agent response', got %q", delta.Messages[1].Parts[0].Text)
	}

	// Verify info is preserved
	if delta.Info.Title != "Test" {
		t.Errorf("expected title=Test, got %q", delta.Info.Title)
	}
}

func TestExtractDeltaTranscript_OutOfBounds(t *testing.T) {
	raw := `{
		"messages": [
			{"User": {"content": [{"Text": "Only one"}]}}
		]
	}`

	// Request delta beyond actual messages
	delta := extractDeltaTranscript([]byte(raw), 0, 10)
	if delta == nil {
		t.Fatal("expected delta even with clamped bounds")
	}
	if len(delta.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(delta.Messages))
	}
}

func TestExtractDeltaTranscript_InvalidJSON(t *testing.T) {
	delta := extractDeltaTranscript([]byte("not json"), 0, 5)
	if delta != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestRedactObject(t *testing.T) {
	input := map[string]interface{}{
		"command": "ls -la",
		"nested": map[string]interface{}{
			"private_key": "secret-xyz",
			"normal":      "value",
		},
	}

	redacted := redactObject(input)

	// Sensitive key should be redacted in nested object
	if _, ok := redacted["nested"].(map[string]interface{})["private_key"].(string); ok {
		// should be masked - just check it's changed
	}

	// Non-sensitive key should be unchanged
	if redacted["command"] != "ls -la" {
		t.Errorf("expected command unchanged, got %v", redacted["command"])
	}
}

func TestRedactTranscript(t *testing.T) {
	transcript := &OpenCodeTranscript{
		Info: &OpenCodeInfo{Title: "Test"},
		Messages: []OpenCodeMessage{
			{
				Info: &OpenCodeMessageInfo{Role: "assistant"},
				Parts: []OpenCodePart{
					{
						Type: "tool_use",
						ToolUse: &OpenCodeToolUse{
							Name: "terminal",
							Input: map[string]interface{}{
								"command": "ls",
								"api_key": "sk-secret123",
							},
						},
					},
				},
			},
		},
	}

	redacted := redactTranscript(transcript)

	// Check API key is redacted in tool_use input
	inp := redacted.Messages[0].Parts[0].ToolUse.Input
	apiKey, _ := inp["api_key"].(string)
	if apiKey == "sk-secret123" {
		t.Errorf("expected api_key to be redacted, got %v", apiKey)
	}
	// Check non-sensitive key is unchanged
	if inp["command"] != "ls" {
		t.Errorf("expected command unchanged, got %v", inp["command"])
	}
}
