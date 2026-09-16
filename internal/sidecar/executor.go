package sidecar

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

type ExecutionResult struct {
	Provider  string
	SessionID string
	Output    string
}

type ConversationExecutor interface {
	Execute(ctx context.Context, task SideTask, prompt, provider string, timeout time.Duration) (ExecutionResult, error)
}

type CLIExecutor struct {
	CodexBinary  string
	ClaudeBinary string
}

func NewCLIExecutor() *CLIExecutor {
	return &CLIExecutor{CodexBinary: "codex", ClaudeBinary: "claude"}
}

func (e *CLIExecutor) Execute(ctx context.Context, task SideTask, prompt, provider string, timeout time.Duration) (ExecutionResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return ExecutionResult{}, errors.New("no pending prompt")
	}
	if timeout <= 0 {
		return ExecutionResult{}, errors.New("execution window is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch provider {
	case "codex":
		return e.executeCodex(ctx, task, prompt)
	case "claude":
		return e.executeClaude(ctx, task, prompt)
	default:
		return ExecutionResult{}, fmt.Errorf("unsupported provider %q", provider)
	}
}

func (e *CLIExecutor) executeCodex(ctx context.Context, task SideTask, prompt string) (ExecutionResult, error) {
	binary := e.CodexBinary
	if binary == "" {
		binary = "codex"
	}
	lastFile, err := os.CreateTemp("", "nightshift-sidecar-codex-*.txt")
	if err != nil {
		return ExecutionResult{}, err
	}
	lastPath := lastFile.Name()
	_ = lastFile.Close()
	defer func() { _ = os.Remove(lastPath) }()

	var args []string
	if task.SessionID == "" {
		args = []string{"exec", "--json", "--sandbox", "workspace-write", "--output-last-message", lastPath}
		if task.ProjectPath != "" {
			args = append(args, "--cd", task.ProjectPath)
		}
		args = append(args, prompt)
	} else {
		args = []string{"exec", "resume", "--json", "--output-last-message", lastPath, task.SessionID, prompt}
	}
	stdout, stderr, err := runCLI(ctx, binary, args, task.ProjectPath)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("codex execution: %w: %s", err, truncateText(stderr, 2000))
	}
	outputBytes, readErr := os.ReadFile(lastPath)
	output := strings.TrimSpace(string(outputBytes))
	if readErr != nil || output == "" {
		output = extractCodexMessage(stdout)
	}
	sessionID := task.SessionID
	if sessionID == "" {
		sessionID = extractCodexThreadID(stdout)
	}
	if sessionID == "" {
		return ExecutionResult{}, errors.New("codex did not return a thread id")
	}
	return ExecutionResult{Provider: "codex", SessionID: sessionID, Output: output}, nil
}

func (e *CLIExecutor) executeClaude(ctx context.Context, task SideTask, prompt string) (ExecutionResult, error) {
	binary := e.ClaudeBinary
	if binary == "" {
		binary = "claude"
	}
	sessionID := task.SessionID
	args := []string{"--print", "--output-format", "json", "--permission-mode", "acceptEdits"}
	if sessionID == "" {
		sessionID = uuid.NewString()
		args = append(args, "--session-id", sessionID)
	} else {
		args = append(args, "--resume", sessionID)
	}
	args = append(args, prompt)
	stdout, stderr, err := runCLI(ctx, binary, args, task.ProjectPath)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("claude execution: %w: %s", err, truncateText(stderr, 2000))
	}
	var response struct {
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
		IsError   bool   `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		return ExecutionResult{}, fmt.Errorf("decode claude result: %w", err)
	}
	if response.IsError {
		return ExecutionResult{}, errors.New(response.Result)
	}
	if response.SessionID != "" {
		sessionID = response.SessionID
	}
	return ExecutionResult{Provider: "claude", SessionID: sessionID, Output: strings.TrimSpace(response.Result)}, nil
}

func runCLI(ctx context.Context, binary string, args []string, dir string) (string, string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	if dir != "" {
		cmd.Dir = filepath.Clean(dir)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func extractCodexThreadID(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var value any
		if json.Unmarshal(scanner.Bytes(), &value) == nil {
			if id := findStringKey(value, "thread_id", "threadId"); id != "" {
				return id
			}
		}
	}
	return ""
}

func extractCodexMessage(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	var latest string
	for scanner.Scan() {
		var value any
		if json.Unmarshal(scanner.Bytes(), &value) == nil {
			if text := findStringKey(value, "text"); text != "" {
				latest = text
			}
		}
	}
	return strings.TrimSpace(latest)
}

func findStringKey(value any, keys ...string) string {
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[key] = true
	}
	var visit func(any) string
	visit = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if wanted[key] {
					if text, ok := child.(string); ok {
						return text
					}
				}
			}
			for _, child := range typed {
				if found := visit(child); found != "" {
					return found
				}
			}
		case []any:
			for _, child := range typed {
				if found := visit(child); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return visit(value)
}

func truncateText(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}
