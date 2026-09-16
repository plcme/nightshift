package sidecar

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/marcus/nightshift/internal/db"
)

var ErrNotFound = errors.New("side task not found")

type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) (*Store, error) {
	if database == nil || database.SQL() == nil {
		return nil, errors.New("db is nil")
	}
	return &Store{db: database}, nil
}

func (s *Store) CreateTask(input CreateTaskInput) (SideTask, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.Title == "" || input.Prompt == "" {
		return SideTask{}, errors.New("title and prompt are required")
	}
	if input.ProviderPreference == "" {
		input.ProviderPreference = ProviderAuto
	}
	if !input.ProviderPreference.Valid() {
		return SideTask{}, fmt.Errorf("invalid provider preference %q", input.ProviderPreference)
	}
	if input.Priority == 0 {
		input.Priority = 50
	}
	if input.Priority < 1 || input.Priority > 100 {
		return SideTask{}, errors.New("priority must be between 1 and 100")
	}

	now := time.Now().UTC()
	task := SideTask{
		ID:                 uuid.NewString(),
		Title:              input.Title,
		ProjectPath:        strings.TrimSpace(input.ProjectPath),
		ProviderPreference: input.ProviderPreference,
		LabelID:            strings.TrimSpace(input.LabelID),
		Status:             StatusQueued,
		Priority:           input.Priority,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	tx, err := s.db.SQL().Begin()
	if err != nil {
		return SideTask{}, fmt.Errorf("begin create task: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(`
		INSERT INTO side_tasks (
			id, title, project_path, provider_preference, provider_used,
			label_id, status, priority, session_id, created_at, updated_at,
			last_error, archived
		) VALUES (?, ?, ?, ?, '', ?, ?, ?, '', ?, ?, '', 0)`,
		task.ID, task.Title, task.ProjectPath, task.ProviderPreference,
		task.LabelID, task.Status, task.Priority, task.CreatedAt, task.UpdatedAt,
	)
	if err != nil {
		return SideTask{}, fmt.Errorf("insert side task: %w", err)
	}
	_, err = tx.Exec(`
		INSERT INTO side_messages (task_id, role, content, created_at)
		VALUES (?, 'user', ?, ?)`, task.ID, input.Prompt, now)
	if err != nil {
		return SideTask{}, fmt.Errorf("insert initial prompt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SideTask{}, fmt.Errorf("commit create task: %w", err)
	}
	return task, nil
}

func (s *Store) ListTasks(includeArchived bool) ([]SideTask, error) {
	query := `SELECT id, title, project_path, provider_preference, provider_used,
		label_id, status, priority, session_id, created_at, updated_at,
		last_run_at, last_error, archived
		FROM side_tasks`
	if !includeArchived {
		query += ` WHERE archived = 0`
	}
	query += ` ORDER BY priority DESC, created_at ASC`

	rows, err := s.db.SQL().Query(query)
	if err != nil {
		return nil, fmt.Errorf("list side tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []SideTask
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, task)
	}
	return result, rows.Err()
}

func (s *Store) GetTask(id string) (SideTask, error) {
	row := s.db.SQL().QueryRow(`SELECT id, title, project_path, provider_preference,
		provider_used, label_id, status, priority, session_id, created_at, updated_at,
		last_run_at, last_error, archived FROM side_tasks WHERE id = ?`, id)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SideTask{}, ErrNotFound
	}
	return task, err
}

func (s *Store) UpdateTask(id string, input UpdateTaskInput) (SideTask, error) {
	task, err := s.GetTask(id)
	if err != nil {
		return SideTask{}, err
	}
	if input.Title != nil {
		if strings.TrimSpace(*input.Title) == "" {
			return SideTask{}, errors.New("title cannot be empty")
		}
		task.Title = strings.TrimSpace(*input.Title)
	}
	if input.ProjectPath != nil {
		task.ProjectPath = strings.TrimSpace(*input.ProjectPath)
	}
	if input.ProviderPreference != nil {
		if !input.ProviderPreference.Valid() {
			return SideTask{}, fmt.Errorf("invalid provider preference %q", *input.ProviderPreference)
		}
		task.ProviderPreference = *input.ProviderPreference
	}
	if input.LabelID != nil {
		task.LabelID = strings.TrimSpace(*input.LabelID)
	}
	if input.Priority != nil {
		if *input.Priority < 1 || *input.Priority > 100 {
			return SideTask{}, errors.New("priority must be between 1 and 100")
		}
		task.Priority = *input.Priority
	}
	if input.Status != nil {
		if !input.Status.Valid() {
			return SideTask{}, fmt.Errorf("invalid task status %q", *input.Status)
		}
		task.Status = *input.Status
		task.Archived = *input.Status == StatusArchived
	}
	task.UpdatedAt = time.Now().UTC()

	_, err = s.db.SQL().Exec(`UPDATE side_tasks SET title = ?, project_path = ?,
		provider_preference = ?, label_id = ?, status = ?, priority = ?, updated_at = ?,
		archived = ? WHERE id = ?`, task.Title, task.ProjectPath, task.ProviderPreference,
		task.LabelID, task.Status, task.Priority, task.UpdatedAt, task.Archived, task.ID)
	if err != nil {
		return SideTask{}, fmt.Errorf("update side task: %w", err)
	}
	return task, nil
}

func (s *Store) AddUserReply(taskID, content string) (Message, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Message{}, errors.New("reply cannot be empty")
	}
	task, err := s.GetTask(taskID)
	if err != nil {
		return Message{}, err
	}
	if task.Archived || task.Status == StatusRunning {
		return Message{}, fmt.Errorf("task cannot accept replies while %s", task.Status)
	}

	now := time.Now().UTC()
	tx, err := s.db.SQL().Begin()
	if err != nil {
		return Message{}, fmt.Errorf("begin reply: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.Exec(`INSERT INTO side_messages (task_id, role, content, created_at)
		VALUES (?, 'user', ?, ?)`, taskID, content, now)
	if err != nil {
		return Message{}, fmt.Errorf("insert reply: %w", err)
	}
	if _, err := tx.Exec(`UPDATE side_tasks SET status = ?, updated_at = ?, last_error = ''
		WHERE id = ?`, StatusQueued, now, taskID); err != nil {
		return Message{}, fmt.Errorf("queue reply: %w", err)
	}
	id, _ := result.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("commit reply: %w", err)
	}
	return Message{ID: id, TaskID: taskID, Role: "user", Content: content, CreatedAt: now}, nil
}

func (s *Store) AddAssistantMessage(taskID, provider, sessionID, content string) (Message, error) {
	now := time.Now().UTC()
	tx, err := s.db.SQL().Begin()
	if err != nil {
		return Message{}, fmt.Errorf("begin assistant message: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`INSERT INTO side_messages
		(task_id, role, content, created_at, dispatched_at) VALUES (?, 'assistant', ?, ?, ?)`,
		taskID, content, now, now)
	if err != nil {
		return Message{}, fmt.Errorf("insert assistant message: %w", err)
	}
	if _, err := tx.Exec(`UPDATE side_messages SET dispatched_at = ?
		WHERE task_id = ? AND role = 'user' AND dispatched_at IS NULL`, now, taskID); err != nil {
		return Message{}, fmt.Errorf("mark prompts dispatched: %w", err)
	}
	if _, err := tx.Exec(`UPDATE side_tasks SET status = ?, provider_used = ?,
		session_id = ?, updated_at = ?, last_run_at = ?, last_error = '' WHERE id = ?`,
		StatusWaitingUser, provider, sessionID, now, now, taskID); err != nil {
		return Message{}, fmt.Errorf("finish side task turn: %w", err)
	}
	id, _ := result.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("commit assistant message: %w", err)
	}
	return Message{ID: id, TaskID: taskID, Role: "assistant", Content: content, CreatedAt: now, DispatchedAt: &now}, nil
}

func (s *Store) ListMessages(taskID string) ([]Message, error) {
	if _, err := s.GetTask(taskID); err != nil {
		return nil, err
	}
	rows, err := s.db.SQL().Query(`SELECT id, task_id, role, content, created_at,
		dispatched_at FROM side_messages WHERE task_id = ? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []Message
	for rows.Next() {
		var msg Message
		var dispatched sql.NullTime
		if err := rows.Scan(&msg.ID, &msg.TaskID, &msg.Role, &msg.Content,
			&msg.CreatedAt, &dispatched); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if dispatched.Valid {
			value := dispatched.Time
			msg.DispatchedAt = &value
		}
		result = append(result, msg)
	}
	return result, rows.Err()
}

func (s *Store) PendingPrompt(taskID string) (string, error) {
	rows, err := s.db.SQL().Query(`SELECT content FROM side_messages
		WHERE task_id = ? AND role = 'user' AND dispatched_at IS NULL ORDER BY id ASC`, taskID)
	if err != nil {
		return "", fmt.Errorf("query pending prompt: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var prompts []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return "", err
		}
		prompts = append(prompts, content)
	}
	return strings.Join(prompts, "\n\n"), rows.Err()
}

func (s *Store) MarkRunning(taskID, provider string) error {
	now := time.Now().UTC()
	result, err := s.db.SQL().Exec(`UPDATE side_tasks SET status = ?, provider_used = ?,
		updated_at = ?, last_error = '' WHERE id = ? AND archived = 0`,
		StatusRunning, provider, now, taskID)
	if err != nil {
		return fmt.Errorf("mark side task running: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkFailed(taskID, message string) error {
	_, err := s.db.SQL().Exec(`UPDATE side_tasks SET status = ?, updated_at = ?,
		last_error = ? WHERE id = ?`, StatusFailed, time.Now().UTC(), message, taskID)
	if err != nil {
		return fmt.Errorf("mark side task failed: %w", err)
	}
	return nil
}

func (s *Store) ListLabels() ([]Label, error) {
	rows, err := s.db.SQL().Query(`SELECT id, name, emoji, color, codex_section,
		default_provider, min_window_remaining_pct, weekly_reserve_pct,
		created_at, updated_at FROM side_labels ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var labels []Label
	for rows.Next() {
		var label Label
		if err := rows.Scan(&label.ID, &label.Name, &label.Emoji, &label.Color,
			&label.CodexSection, &label.DefaultProvider, &label.MinWindowRemainingPct,
			&label.WeeklyReservePct, &label.CreatedAt, &label.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan label: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, rows.Err()
}

func (s *Store) GetLabel(id string) (Label, error) {
	row := s.db.SQL().QueryRow(`SELECT id, name, emoji, color, codex_section,
		default_provider, min_window_remaining_pct, weekly_reserve_pct,
		created_at, updated_at FROM side_labels WHERE id = ?`, id)
	var label Label
	if err := row.Scan(&label.ID, &label.Name, &label.Emoji, &label.Color,
		&label.CodexSection, &label.DefaultProvider, &label.MinWindowRemainingPct,
		&label.WeeklyReservePct, &label.CreatedAt, &label.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Label{}, ErrNotFound
		}
		return Label{}, fmt.Errorf("get label: %w", err)
	}
	return label, nil
}

func (s *Store) UpsertLabel(label Label) (Label, error) {
	label.ID = strings.TrimSpace(label.ID)
	label.Name = strings.TrimSpace(label.Name)
	if label.ID == "" {
		label.ID = uuid.NewString()
	}
	if label.Name == "" {
		return Label{}, errors.New("label name is required")
	}
	if label.DefaultProvider == "" {
		label.DefaultProvider = ProviderAuto
	}
	if !label.DefaultProvider.Valid() {
		return Label{}, errors.New("invalid default provider")
	}
	if label.Color == "" {
		label.Color = "#64748b"
	}
	now := time.Now().UTC()
	if label.CreatedAt.IsZero() {
		label.CreatedAt = now
	}
	label.UpdatedAt = now
	_, err := s.db.SQL().Exec(`INSERT INTO side_labels (
		id, name, emoji, color, codex_section, default_provider,
		min_window_remaining_pct, weekly_reserve_pct, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET name = excluded.name, emoji = excluded.emoji,
		color = excluded.color, codex_section = excluded.codex_section,
		default_provider = excluded.default_provider,
		min_window_remaining_pct = excluded.min_window_remaining_pct,
		weekly_reserve_pct = excluded.weekly_reserve_pct, updated_at = excluded.updated_at`,
		label.ID, label.Name, label.Emoji, label.Color, label.CodexSection,
		label.DefaultProvider, label.MinWindowRemainingPct, label.WeeklyReservePct,
		label.CreatedAt, label.UpdatedAt)
	if err != nil {
		return Label{}, fmt.Errorf("upsert label: %w", err)
	}
	return label, nil
}

func (s *Store) SetSetting(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("setting key is required")
	}
	_, err := s.db.SQL().Exec(`INSERT INTO side_settings (key, value, updated_at)
		VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value,
		updated_at = excluded.updated_at`, key, value, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("set side setting: %w", err)
	}
	return nil
}

func (s *Store) GetSetting(key, fallback string) (string, error) {
	row := s.db.SQL().QueryRow(`SELECT value FROM side_settings WHERE key = ?`, key)
	var value string
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fallback, nil
		}
		return "", fmt.Errorf("get side setting: %w", err)
	}
	return value, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (SideTask, error) {
	var task SideTask
	var lastRun sql.NullTime
	var archived int
	if err := row.Scan(&task.ID, &task.Title, &task.ProjectPath,
		&task.ProviderPreference, &task.ProviderUsed, &task.LabelID, &task.Status,
		&task.Priority, &task.SessionID, &task.CreatedAt, &task.UpdatedAt,
		&lastRun, &task.LastError, &archived); err != nil {
		return SideTask{}, err
	}
	if lastRun.Valid {
		value := lastRun.Time
		task.LastRunAt = &value
	}
	task.Archived = archived != 0
	return task, nil
}
