// Package codexapp provides the small App Server surface needed by sidecar.
package codexapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

type Window struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int64   `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type Snapshot struct {
	LimitID   string  `json:"limitId"`
	Primary   *Window `json:"primary"`
	Secondary *Window `json:"secondary"`
}

type rateLimitResult struct {
	RateLimits          *Snapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]*Snapshot `json:"rateLimitsByLimitId"`
}

type rpcMessage struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// ReadRateLimits starts a short-lived Codex App Server and reads live limits.
func ReadRateLimits(ctx context.Context, binary string) ([]Snapshot, error) {
	if binary == "" {
		binary = "codex"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex app-server stdout: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	if err := send(stdin, map[string]any{
		"method": "initialize", "id": 1,
		"params": map[string]any{
			"clientInfo":   map[string]string{"name": "nightshift-sidecar", "version": "0.1.0"},
			"capabilities": map[string]bool{"experimentalApi": true},
		},
	}); err != nil {
		return nil, err
	}
	if _, err := waitForResult(scanner, 1); err != nil {
		return nil, withStderr(err, stderr.String())
	}
	if err := send(stdin, map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	if err := send(stdin, map[string]any{"method": "account/rateLimits/read", "id": 2, "params": map[string]any{}}); err != nil {
		return nil, err
	}
	raw, err := waitForResult(scanner, 2)
	if err != nil {
		return nil, withStderr(err, stderr.String())
	}
	var result rateLimitResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode codex rate limits: %w", err)
	}

	seen := map[string]bool{}
	var snapshots []Snapshot
	appendSnapshot := func(snapshot *Snapshot) {
		if snapshot == nil {
			return
		}
		key := snapshot.LimitID
		if key == "" {
			key = fmt.Sprintf("%p", snapshot)
		}
		if seen[key] {
			return
		}
		seen[key] = true
		snapshots = append(snapshots, *snapshot)
	}
	appendSnapshot(result.RateLimits)
	for _, snapshot := range result.RateLimitsByLimitID {
		appendSnapshot(snapshot)
	}
	if len(snapshots) == 0 {
		return nil, errors.New("codex returned no rate-limit windows")
	}
	return snapshots, nil
}

func send(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write codex app-server request: %w", err)
	}
	return nil
}

func waitForResult(scanner *bufio.Scanner, id int) (json.RawMessage, error) {
	for scanner.Scan() {
		var message rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			continue
		}
		if message.ID != id {
			continue
		}
		if len(message.Error) > 0 && string(message.Error) != "null" {
			return nil, fmt.Errorf("codex app-server error: %s", message.Error)
		}
		return message.Result, nil
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read codex app-server: %w", err)
	}
	return nil, io.EOF
}

func withStderr(err error, stderr string) error {
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, stderr)
}
