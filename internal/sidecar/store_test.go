package sidecar

import (
	"path/filepath"
	"testing"

	"github.com/marcus/nightshift/internal/db"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := NewStore(database)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestTaskConversationLifecycle(t *testing.T) {
	store := newTestStore(t)
	task, err := store.CreateTask(CreateTaskInput{
		Title: "Ship side project", Prompt: "Implement the first slice.",
		ProviderPreference: ProviderAuto, LabelID: "side", Priority: 80,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.Status != StatusQueued {
		t.Fatalf("status = %q, want queued", task.Status)
	}

	prompt, err := store.PendingPrompt(task.ID)
	if err != nil || prompt != "Implement the first slice." {
		t.Fatalf("pending prompt = %q, err=%v", prompt, err)
	}
	if _, err := store.AddAssistantMessage(task.ID, "codex", "session-1", "First slice is ready."); err != nil {
		t.Fatalf("assistant message: %v", err)
	}
	task, _ = store.GetTask(task.ID)
	if task.Status != StatusWaitingUser || task.SessionID != "session-1" {
		t.Fatalf("task after run = %#v", task)
	}

	if _, err := store.AddUserReply(task.ID, "Continue with tests."); err != nil {
		t.Fatalf("reply: %v", err)
	}
	task, _ = store.GetTask(task.ID)
	if task.Status != StatusQueued {
		t.Fatalf("reply should requeue task, got %q", task.Status)
	}
	prompt, _ = store.PendingPrompt(task.ID)
	if prompt != "Continue with tests." {
		t.Fatalf("continuation prompt = %q", prompt)
	}
}
