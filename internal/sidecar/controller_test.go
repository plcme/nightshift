package sidecar

import (
	"context"
	"testing"
	"time"
)

type fakeQuotaSource struct{ quotas []ProviderQuota }

func (f fakeQuotaSource) Read(context.Context) []ProviderQuota { return f.quotas }

type fakeConversationExecutor struct{ calls int }

func (f *fakeConversationExecutor) Execute(_ context.Context, _ SideTask, _ string, provider string, _ time.Duration) (ExecutionResult, error) {
	f.calls++
	return ExecutionResult{Provider: provider, SessionID: "session-1", Output: "done"}, nil
}

func TestControllerRequiresExplicitSchedulerEnable(t *testing.T) {
	store := newTestStore(t)
	task, err := store.CreateTask(CreateTaskInput{Title: "queued", Prompt: "do it", ProjectPath: t.TempDir(), ProviderPreference: ProviderCodex, LabelID: "side"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	source := fakeQuotaSource{quotas: []ProviderQuota{{Provider: "codex", Connected: true,
		FiveHourRemainingPct: 50, WeeklyRemainingPct: 70, FiveHourResetAt: now.Add(30 * time.Minute), ObservedAt: now}}}
	executor := &fakeConversationExecutor{}
	controller := NewController(store, source, executor, DefaultHarvestPolicy())

	if err := controller.RefreshAndRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 0 {
		t.Fatal("default scheduler state must not execute tasks")
	}
	if err := store.SetSetting("scheduler_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := controller.RefreshAndRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 0 {
		t.Fatal("first eligible observation must only arm the task")
	}
	if err := controller.RefreshAndRun(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	got, _ := store.GetTask(task.ID)
	if got.Status != StatusWaitingUser || got.SessionID != "session-1" {
		t.Fatalf("unexpected completed turn state: %#v", got)
	}
}
