package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/nightshift/internal/codexapp"
	"github.com/marcus/nightshift/internal/config"
	"github.com/marcus/nightshift/internal/tmux"
)

type QuotaSource interface {
	Read(ctx context.Context) []ProviderQuota
}

type LiveQuotaSource struct {
	config      *config.Config
	codexBinary string
	claudeProbe func(context.Context) (tmux.UsageResult, error)
}

func NewLiveQuotaSource(cfg *config.Config) *LiveQuotaSource {
	return &LiveQuotaSource{config: cfg, codexBinary: "codex", claudeProbe: tmux.ScrapeClaudeUsage}
}

func (s *LiveQuotaSource) Read(ctx context.Context) []ProviderQuota {
	now := time.Now()
	result := make([]ProviderQuota, 0, 2)
	if s.config.Providers.Codex.Enabled {
		result = append(result, s.readCodex(ctx, now))
	}
	if s.config.Providers.Claude.Enabled {
		result = append(result, s.readClaude(ctx, now))
	}
	return result
}

func (s *LiveQuotaSource) readCodex(ctx context.Context, now time.Time) ProviderQuota {
	quota := ProviderQuota{Provider: "codex", ObservedAt: now}
	if _, err := exec.LookPath(s.codexBinary); err != nil {
		quota.Error = "codex CLI not installed"
		return quota
	}
	snapshots, err := codexapp.ReadRateLimits(ctx, s.codexBinary)
	if err != nil {
		quota.Error = err.Error()
		return quota
	}
	five, weekly := classifyCodexWindows(snapshots)
	if five == nil {
		quota.Error = "5h quota window unavailable"
		return quota
	}
	quota.Connected = true
	quota.FiveHourRemainingPct = clampPercent(100 - five.UsedPercent)
	quota.FiveHourResetAt = time.Unix(five.ResetsAt, 0)
	if weekly != nil {
		quota.WeeklyRemainingPct = clampPercent(100 - weekly.UsedPercent)
	} else {
		quota.WeeklyRemainingPct = 100
	}
	return quota
}

func (s *LiveQuotaSource) readClaude(ctx context.Context, now time.Time) ProviderQuota {
	quota := ProviderQuota{Provider: "claude", ObservedAt: now}
	claudeBinary, err := exec.LookPath("claude")
	if err != nil {
		quota.Error = "claude CLI not installed"
		return quota
	}
	if loggedIn, known := claudeLoginStatus(ctx, claudeBinary); known && !loggedIn {
		quota.Error = "claude CLI is not logged in"
		return quota
	}
	usage, err := s.claudeProbe(ctx)
	if err != nil {
		quota.Error = err.Error()
		return quota
	}
	resetAt, err := ParseResetTime(usage.SessionResetTime, now)
	if err != nil {
		quota.Error = err.Error()
		return quota
	}
	quota.Connected = true
	quota.FiveHourRemainingPct = clampPercent(100 - usage.SessionPct)
	quota.WeeklyRemainingPct = clampPercent(100 - usage.WeeklyPct)
	quota.FiveHourResetAt = resetAt
	return quota
}

func claudeLoginStatus(ctx context.Context, binary string) (loggedIn, known bool) {
	statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, _ := exec.CommandContext(statusCtx, binary, "auth", "status").CombinedOutput()
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(string(output))
	if strings.Contains(compact, `"loggedIn":false`) {
		return false, true
	}
	if strings.Contains(compact, `"loggedIn":true`) {
		return true, true
	}
	if start, end := strings.IndexByte(string(output), '{'), strings.LastIndexByte(string(output), '}'); start >= 0 && end > start {
		output = output[start : end+1]
	}
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return false, false
	}
	// Claude exits non-zero when status is valid JSON but loggedIn is false.
	return status.LoggedIn, true
}

func classifyCodexWindows(snapshots []codexapp.Snapshot) (fiveHour, weekly *codexapp.Window) {
	for i := range snapshots {
		windows := []*codexapp.Window{snapshots[i].Primary, snapshots[i].Secondary}
		for _, window := range windows {
			if window == nil {
				continue
			}
			switch {
			case window.WindowDurationMins > 0 && window.WindowDurationMins <= 600:
				if fiveHour == nil || window.WindowDurationMins < fiveHour.WindowDurationMins {
					fiveHour = window
				}
			case window.WindowDurationMins >= 24*60:
				if weekly == nil || window.WindowDurationMins > weekly.WindowDurationMins {
					weekly = window
				}
			}
		}
	}
	return fiveHour, weekly
}

func ParseResetTime(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("reset time unavailable")
	}
	location := now.Location()
	zoneRE := regexp.MustCompile(`\s*\(([^)]+)\)\s*$`)
	if match := zoneRE.FindStringSubmatch(value); len(match) == 2 {
		if loaded, err := time.LoadLocation(match[1]); err == nil {
			location = loaded
		}
		value = strings.TrimSpace(zoneRE.ReplaceAllString(value, ""))
	}

	localNow := now.In(location)
	for _, layout := range []string{"3:04pm", "3pm", "15:04"} {
		if parsed, err := time.ParseInLocation(layout, strings.ToLower(value), location); err == nil {
			candidate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location)
			if !candidate.After(localNow) {
				candidate = candidate.AddDate(0, 0, 1)
			}
			return candidate, nil
		}
	}

	if strings.Contains(value, " on ") {
		parts := strings.SplitN(value, " on ", 2)
		for _, layout := range []string{"15:04 2 Jan 2006", "3:04pm 2 Jan 2006"} {
			withYear := fmt.Sprintf("%s %s %d", strings.ToLower(parts[0]), parts[1], localNow.Year())
			if parsed, err := time.ParseInLocation(layout, withYear, location); err == nil {
				if !parsed.After(localNow) {
					parsed = parsed.AddDate(1, 0, 0)
				}
				return parsed, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("unsupported reset time %q", value)
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func parseInt(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}
