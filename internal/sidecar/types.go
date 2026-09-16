// Package sidecar implements persistent, quota-triggered side tasks.
package sidecar

import "time"

type ProviderPreference string

const (
	ProviderAuto   ProviderPreference = "auto"
	ProviderCodex  ProviderPreference = "codex"
	ProviderClaude ProviderPreference = "claude"
)

func (p ProviderPreference) Valid() bool {
	return p == ProviderAuto || p == ProviderCodex || p == ProviderClaude
}

type TaskStatus string

const (
	StatusStaged      TaskStatus = "staged"
	StatusQueued      TaskStatus = "queued"
	StatusRunning     TaskStatus = "running"
	StatusWaitingUser TaskStatus = "waiting_user"
	StatusPaused      TaskStatus = "paused"
	StatusCompleted   TaskStatus = "completed"
	StatusFailed      TaskStatus = "failed"
	StatusArchived    TaskStatus = "archived"
)

func (s TaskStatus) Valid() bool {
	switch s {
	case StatusStaged, StatusQueued, StatusRunning, StatusWaitingUser,
		StatusPaused, StatusCompleted, StatusFailed, StatusArchived:
		return true
	default:
		return false
	}
}

type SideTask struct {
	ID                      string             `json:"id"`
	Title                   string             `json:"title"`
	ProjectPath             string             `json:"project_path"`
	ProviderPreference      ProviderPreference `json:"provider_preference"`
	CodexModel              string             `json:"codex_model"`
	ClaudeModel             string             `json:"claude_model"`
	MinFiveHourRemainingPct float64            `json:"min_five_hour_remaining_pct"`
	MaxRunMinutes           int                `json:"max_run_minutes"`
	ProviderUsed            string             `json:"provider_used"`
	LabelID                 string             `json:"label_id"`
	Status                  TaskStatus         `json:"status"`
	Priority                int                `json:"priority"`
	SessionID               string             `json:"session_id"`
	CreatedAt               time.Time          `json:"created_at"`
	UpdatedAt               time.Time          `json:"updated_at"`
	LastRunAt               *time.Time         `json:"last_run_at,omitempty"`
	LastError               string             `json:"last_error"`
	Archived                bool               `json:"archived"`
}

type Message struct {
	ID           int64      `json:"id"`
	TaskID       string     `json:"task_id"`
	Role         string     `json:"role"`
	Content      string     `json:"content"`
	CreatedAt    time.Time  `json:"created_at"`
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`
}

type Label struct {
	ID                    string             `json:"id"`
	Name                  string             `json:"name"`
	Emoji                 string             `json:"emoji"`
	Color                 string             `json:"color"`
	CodexSection          string             `json:"codex_section"`
	DefaultProvider       ProviderPreference `json:"default_provider"`
	MinWindowRemainingPct float64            `json:"min_window_remaining_pct"`
	WeeklyReservePct      float64            `json:"weekly_reserve_pct"`
	CreatedAt             time.Time          `json:"created_at"`
	UpdatedAt             time.Time          `json:"updated_at"`
}

type CreateTaskInput struct {
	Title                   string             `json:"title"`
	Prompt                  string             `json:"prompt"`
	ProjectPath             string             `json:"project_path"`
	ProviderPreference      ProviderPreference `json:"provider_preference"`
	CodexModel              string             `json:"codex_model"`
	ClaudeModel             string             `json:"claude_model"`
	MinFiveHourRemainingPct float64            `json:"min_five_hour_remaining_pct"`
	MaxRunMinutes           int                `json:"max_run_minutes"`
	LabelID                 string             `json:"label_id"`
	Priority                int                `json:"priority"`
}

type UpdateTaskInput struct {
	Title                   *string             `json:"title,omitempty"`
	ProjectPath             *string             `json:"project_path,omitempty"`
	ProviderPreference      *ProviderPreference `json:"provider_preference,omitempty"`
	CodexModel              *string             `json:"codex_model,omitempty"`
	ClaudeModel             *string             `json:"claude_model,omitempty"`
	MinFiveHourRemainingPct *float64            `json:"min_five_hour_remaining_pct,omitempty"`
	MaxRunMinutes           *int                `json:"max_run_minutes,omitempty"`
	LabelID                 *string             `json:"label_id,omitempty"`
	Priority                *int                `json:"priority,omitempty"`
	Status                  *TaskStatus         `json:"status,omitempty"`
}
