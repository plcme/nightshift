package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

var (
	// ErrTmuxNotFound indicates tmux is not installed.
	ErrTmuxNotFound = errors.New("tmux not found")
)

// UsageResult captures scraped usage metadata.
type UsageResult struct {
	Provider         string
	SessionPct       float64
	WeeklyPct        float64
	SessionResetTime string // e.g. "9pm (America/Los_Angeles)" or "01:18 on 5 Feb"
	WeeklyResetTime  string // e.g. "Feb 8 at 10am (America/Los_Angeles)" or "20:08 on 9 Feb"
	ScrapedAt        time.Time
	RawOutput        string
}

// ScrapeClaudeUsage starts Claude in tmux, runs /usage, and parses weekly usage percent.
func ScrapeClaudeUsage(ctx context.Context) (UsageResult, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return scrapeClaudeUsagePTY(ctx)
	}

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	session := NewSession(uniqueSessionName("claude"), WithSize(120, 40))
	if err := session.Start(ctx); err != nil {
		return UsageResult{}, err
	}
	defer func() { _ = session.Kill(context.Background()) }()

	// Launch Claude Code
	if err := session.SendKeys(ctx, "claude", "Enter"); err != nil {
		return UsageResult{}, err
	}

	// Wait for the TUI to render before sending any commands
	startupOutput, err := waitForSubstantialContent(ctx, session, 20*time.Second)
	if err != nil {
		return UsageResult{}, fmt.Errorf("claude startup: %w", err)
	}

	// Handle trust prompt if present in startup output
	if strings.Contains(StripANSI(startupOutput), "Do you trust") {
		if err := session.SendKeys(ctx, "Enter"); err != nil {
			return UsageResult{}, err
		}
		if err := ctxSleep(ctx, 3*time.Second); err != nil {
			return UsageResult{}, err
		}
	}

	// Brief pause to ensure CLI is ready for input
	if err := ctxSleep(ctx, 1*time.Second); err != nil {
		return UsageResult{}, err
	}

	// Type /usage and wait for autocomplete to populate before pressing Enter.
	// Claude Code shows a command picker when slash commands are typed.
	if err := session.SendKeys(ctx, "/usage"); err != nil {
		return UsageResult{}, err
	}
	if err := ctxSleep(ctx, 500*time.Millisecond); err != nil {
		return UsageResult{}, err
	}
	if err := session.SendKeys(ctx, "Enter"); err != nil {
		return UsageResult{}, err
	}

	// Wait for usage output
	output, err := session.WaitForPattern(ctx, claudeWeekRegex, 15*time.Second, 300*time.Millisecond, "-S", "-200")
	if err != nil {
		return UsageResult{}, err
	}

	clean := StripANSI(output)
	weeklyPct, err := parseClaudeWeeklyPct(clean)
	if err != nil {
		return UsageResult{}, err
	}

	sessionReset, weeklyReset := parseClaudeResetTimes(clean)
	sessionPct, err := parseClaudeSessionPct(clean)
	if err != nil {
		return UsageResult{}, err
	}

	return UsageResult{
		Provider:         "claude",
		SessionPct:       sessionPct,
		WeeklyPct:        weeklyPct,
		SessionResetTime: sessionReset,
		WeeklyResetTime:  weeklyReset,
		ScrapedAt:        time.Now(),
		RawOutput:        clean,
	}, nil
}

// scrapeClaudeUsagePTY is the tmux-free fallback used on desktop machines.
// It opens the real Claude TUI in an isolated pseudo-terminal and submits the
// read-only /usage command; it never sends a model prompt.
func scrapeClaudeUsagePTY(ctx context.Context) (UsageResult, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return UsageResult{}, fmt.Errorf("claude not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return UsageResult{}, fmt.Errorf("start claude pty: %w", err)
	}
	defer func() {
		_ = terminal.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	var captured lockedBuffer
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&captured, terminal)
		close(copyDone)
	}()

	startup, err := waitForPTY(ctx, &captured, 20*time.Second, func(clean string) bool {
		return countNonEmptyLines(clean) > 5 || strings.Contains(clean, "Do you trust") ||
			strings.Contains(strings.ToLower(clean), "login")
	})
	if err != nil {
		return UsageResult{}, fmt.Errorf("claude pty startup: %w", err)
	}
	lower := strings.ToLower(startup)
	if strings.Contains(lower, "not logged in") || strings.Contains(lower, "run /login") || strings.Contains(lower, "please log in") {
		return UsageResult{}, errors.New("claude CLI is not logged in")
	}
	if strings.Contains(startup, "Do you trust") {
		if _, err := terminal.Write([]byte("\r")); err != nil {
			return UsageResult{}, err
		}
		if err := ctxSleep(ctx, 2*time.Second); err != nil {
			return UsageResult{}, err
		}
	}
	if _, err := terminal.Write([]byte("/usage")); err != nil {
		return UsageResult{}, err
	}
	if err := ctxSleep(ctx, 500*time.Millisecond); err != nil {
		return UsageResult{}, err
	}
	if _, err := terminal.Write([]byte("\r")); err != nil {
		return UsageResult{}, err
	}

	output, err := waitForPTY(ctx, &captured, 20*time.Second, func(clean string) bool {
		_, sessionErr := parseClaudeSessionPct(clean)
		_, weeklyErr := parseClaudeWeeklyPct(clean)
		return sessionErr == nil && weeklyErr == nil
	})
	if err != nil {
		return UsageResult{}, fmt.Errorf("claude /usage: %w", err)
	}
	sessionPct, err := parseClaudeSessionPct(output)
	if err != nil {
		return UsageResult{}, err
	}
	weeklyPct, err := parseClaudeWeeklyPct(output)
	if err != nil {
		return UsageResult{}, err
	}
	sessionReset, weeklyReset := parseClaudeResetTimes(output)
	return UsageResult{Provider: "claude", SessionPct: sessionPct, WeeklyPct: weeklyPct,
		SessionResetTime: sessionReset, WeeklyResetTime: weeklyReset,
		ScrapedAt: time.Now(), RawOutput: output}, nil
}

type lockedBuffer struct {
	mu sync.RWMutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(value)
}

func (b *lockedBuffer) String() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.b.String()
}

func waitForPTY(ctx context.Context, buffer *lockedBuffer, timeout time.Duration, ready func(string) bool) (string, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var latest string
	for {
		select {
		case <-ctx.Done():
			return latest, ctx.Err()
		case <-deadline.C:
			return latest, errors.New("timed out waiting for Claude TUI")
		case <-ticker.C:
			latest = StripANSI(buffer.String())
			if ready(latest) {
				return latest, nil
			}
		}
	}
}

// ScrapeCodexUsage starts Codex in tmux, runs /status, and parses weekly usage percent.
func ScrapeCodexUsage(ctx context.Context) (UsageResult, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return UsageResult{}, ErrTmuxNotFound
	}

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	session := NewSession(uniqueSessionName("codex"), WithSize(120, 40))
	if err := session.Start(ctx); err != nil {
		return UsageResult{}, err
	}
	defer func() { _ = session.Kill(context.Background()) }()

	// Launch Codex
	if err := session.SendKeys(ctx, "codex", "Enter"); err != nil {
		return UsageResult{}, err
	}

	// Wait for the TUI to render
	startupOutput, err := waitForSubstantialContent(ctx, session, 20*time.Second)
	if err != nil {
		return UsageResult{}, fmt.Errorf("codex startup: %w", err)
	}

	// Handle Codex-specific prompts from startup output
	clean := StripANSI(startupOutput)
	if strings.Contains(clean, "Update available") {
		if err := session.SendKeys(ctx, "Down", "Enter"); err != nil {
			return UsageResult{}, err
		}
		if err := ctxSleep(ctx, 3*time.Second); err != nil {
			return UsageResult{}, err
		}
		// Re-capture after update prompt dismissed
		startupOutput, _ = session.CapturePane(ctx, "-S", "-50")
		clean = StripANSI(startupOutput)
	}
	if strings.Contains(clean, "allow Codex to work") {
		if err := session.SendKeys(ctx, "Enter"); err != nil {
			return UsageResult{}, err
		}
		if err := ctxSleep(ctx, 3*time.Second); err != nil {
			return UsageResult{}, err
		}
	}

	// Brief pause to ensure CLI is ready for input
	if err := ctxSleep(ctx, 1*time.Second); err != nil {
		return UsageResult{}, err
	}

	// Type /status and wait for autocomplete before pressing Enter.
	if err := session.SendKeys(ctx, "/status"); err != nil {
		return UsageResult{}, err
	}
	if err := ctxSleep(ctx, 500*time.Millisecond); err != nil {
		return UsageResult{}, err
	}
	if err := session.SendKeys(ctx, "Enter"); err != nil {
		return UsageResult{}, err
	}

	// Wait for status output
	output, err := session.WaitForPattern(ctx, codexWeekRegex, 15*time.Second, 300*time.Millisecond, "-S", "-200")
	if err != nil {
		return UsageResult{}, err
	}

	cleanOutput := StripANSI(output)
	weeklyPct, err := parseCodexWeeklyPct(cleanOutput)
	if err != nil {
		return UsageResult{}, err
	}

	sessionReset, weeklyReset := parseCodexResetTimes(cleanOutput)
	sessionPct, err := parseCodexSessionPct(cleanOutput)
	if err != nil {
		return UsageResult{}, err
	}

	return UsageResult{
		Provider:         "codex",
		SessionPct:       sessionPct,
		WeeklyPct:        weeklyPct,
		SessionResetTime: sessionReset,
		WeeklyResetTime:  weeklyReset,
		ScrapedAt:        time.Now(),
		RawOutput:        cleanOutput,
	}, nil
}

var claudeWeekRegex = regexp.MustCompile(`(?i)current\s+week`)
var codexWeekRegex = regexp.MustCompile(`(?i)weekly\s+limit`)

func parseClaudeSessionPct(output string) (float64, error) {
	return parseWindowPct(output, `current\s+session`)
}

func parseCodexSessionPct(output string) (float64, error) {
	return parseWindowPct(output, `5h\s+limit`)
}

func parseWindowPct(output, label string) (float64, error) {
	output = StripANSI(output)
	re := regexp.MustCompile(`(?is)` + label + `.*?(\d{1,3}(?:\.\d+)?)%\s*(left|used)?`)
	match := re.FindStringSubmatch(output)
	if len(match) < 2 {
		return 0, fmt.Errorf("%s percentage not found", label)
	}
	pct, err := parsePct(match[1])
	if err != nil {
		return 0, err
	}
	if len(match) >= 3 && strings.EqualFold(match[2], "left") {
		return 100 - pct, nil
	}
	return pct, nil
}

func parseClaudeWeeklyPct(output string) (float64, error) {
	output = StripANSI(output)
	// Match "Current week" followed by a percentage, possibly on the next line.
	// The (?s) flag makes . match newlines so the pattern crosses lines.
	re := regexp.MustCompile(`(?is)current\s+week\s*\(all\s+models\).*?(\d{1,3}(?:\.\d+)?)%`)
	if match := re.FindStringSubmatch(output); len(match) == 2 {
		return parsePct(match[1])
	}
	// Fallback: any "Current week" header followed by a percentage
	re2 := regexp.MustCompile(`(?is)current\s+week.*?(\d{1,3}(?:\.\d+)?)%`)
	if match := re2.FindStringSubmatch(output); len(match) == 2 {
		return parsePct(match[1])
	}
	return 0, errors.New("claude weekly usage percent not found")
}

func parseCodexWeeklyPct(output string) (float64, error) {
	output = StripANSI(output)
	// Codex /status shows "77% left" -- extract the number and qualifier.
	re := regexp.MustCompile(`(?i)weekly\s+limit[^\n]*?(\d{1,3}(?:\.\d+)?)%\s*(left|used)?`)
	if match := re.FindStringSubmatch(output); len(match) >= 2 {
		pct, err := parsePct(match[1])
		if err != nil {
			return 0, err
		}
		// Convert "left" to "used" percentage
		if len(match) >= 3 && strings.EqualFold(match[2], "left") {
			pct = 100 - pct
		}
		return pct, nil
	}
	return 0, errors.New("codex weekly usage percent not found")
}

func parsePct(value string) (float64, error) {
	pct, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, fmt.Errorf("parse percent: %w", err)
	}
	if pct < 0 || pct > 100 {
		return 0, fmt.Errorf("percent out of range: %.2f", pct)
	}
	return pct, nil
}

func uniqueSessionName(provider string) string {
	return fmt.Sprintf("nightshift-usage-%s-%d", provider, time.Now().UnixNano())
}

// waitForSubstantialContent polls the pane until it has more than a bare
// shell prompt's worth of content, indicating the CLI TUI has rendered.
func waitForSubstantialContent(ctx context.Context, session *Session, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastOutput string
	for {
		select {
		case <-ctx.Done():
			return lastOutput, fmt.Errorf("timeout waiting for CLI (%d non-empty lines seen)",
				countNonEmptyLines(StripANSI(lastOutput)))
		case <-ticker.C:
			output, err := session.CapturePane(ctx, "-S", "-50")
			if err != nil {
				continue
			}
			lastOutput = output
			if countNonEmptyLines(StripANSI(output)) > 5 {
				return output, nil
			}
		}
	}
}

// countNonEmptyLines returns the number of non-blank lines in s.
func countNonEmptyLines(s string) int {
	count := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// parseClaudeResetTimes extracts session and weekly reset times from Claude /usage output.
// Claude shows:
//
//	Current session
//	... 0% used
//	Resets 9pm (America/Los_Angeles)
//
//	Current week (all models)
//	... 59% used
//	Resets Feb 8 at 10am (America/Los_Angeles)
func parseClaudeResetTimes(output string) (sessionReset, weeklyReset string) {
	output = StripANSI(output)

	// Session reset: appears after "Current session" and before "Current week".
	// Format: "Resets 9pm (America/Los_Angeles)" or "Resets 8:59pm (America/Los_Angeles)"
	sessionRe := regexp.MustCompile(`(?is)current\s+session.*?resets\s+(.+?)(?:\n|$)`)
	if m := sessionRe.FindStringSubmatch(output); len(m) == 2 {
		sessionReset = strings.TrimSpace(m[1])
	}

	// Weekly reset: appears after "Current week (all models)".
	// Format: "Resets Feb 8 at 10am (America/Los_Angeles)" or "Resets Feb 8 at 9:59am (America/Los_Angeles)"
	weeklyRe := regexp.MustCompile(`(?is)current\s+week\s*\(all\s+models\).*?resets\s+(.+?)(?:\n|$)`)
	if m := weeklyRe.FindStringSubmatch(output); len(m) == 2 {
		weeklyReset = strings.TrimSpace(m[1])
	}

	return sessionReset, weeklyReset
}

// parseCodexResetTimes extracts session (5h) and weekly reset times from Codex /status output.
// Codex shows:
//
//	5h limit:     [...] 100% left (resets 02:50 on 8 Feb)
//	Weekly limit: [...] 13% left (resets 20:08 on 9 Feb)
//
// Older format omitted the date on the 5h line: (resets 20:15)
func parseCodexResetTimes(output string) (sessionReset, weeklyReset string) {
	output = StripANSI(output)

	// Session (5h) reset: "(resets HH:MM)" or "(resets HH:MM on D Mon)"
	sessionRe := regexp.MustCompile(`(?i)5h\s+limit[^\n]*\(resets\s+(\d{1,2}:\d{2}(?:\s+on\s+\d{1,2}\s+\w+)?)\)`)
	if m := sessionRe.FindStringSubmatch(output); len(m) == 2 {
		sessionReset = m[1]
	}

	// Weekly reset: "(resets HH:MM on D Mon)"
	weeklyRe := regexp.MustCompile(`(?i)weekly\s+limit[^\n]*\(resets\s+(\d{1,2}:\d{2}\s+on\s+\d{1,2}\s+\w+)\)`)
	if m := weeklyRe.FindStringSubmatch(output); len(m) == 2 {
		weeklyReset = m[1]
	}

	// Fallback: if primary weekly regex didn't match, find the last "(resets HH:MM on D Mon)"
	// in the output. The weekly line is always shown last in Codex /status.
	// Only use the fallback when we find a match distinct from the session reset
	// (avoids misidentifying the 5h line as weekly when it's the only line).
	if weeklyReset == "" {
		fallbackRe := regexp.MustCompile(`\(resets\s+(\d{1,2}:\d{2}\s+on\s+\d{1,2}\s+\w+)\)`)
		matches := fallbackRe.FindAllStringSubmatch(output, -1)
		if len(matches) > 0 {
			candidate := matches[len(matches)-1][1]
			if candidate != sessionReset {
				weeklyReset = candidate
			}
		}
	}

	return sessionReset, weeklyReset
}

// ctxSleep pauses for d or until ctx is cancelled.
func ctxSleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
