package sidecar

import (
	"testing"
	"time"
)

func TestEvaluateQuotaHarvestWindow(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	policy := DefaultHarvestPolicy()
	quota := ProviderQuota{
		Provider: "codex", Connected: true, FiveHourRemainingPct: 45,
		WeeklyRemainingPct: 60, FiveHourResetAt: now.Add(30 * time.Minute), ObservedAt: now,
	}
	got := EvaluateQuota(now, quota, policy)
	if !got.Eligible {
		t.Fatalf("expected eligible, got %q", got.Reason)
	}
	if got.RunFor != 22*time.Minute {
		t.Fatalf("run_for = %v, want 22m", got.RunFor)
	}
}

func TestEvaluateQuotaProtectsNewWindow(t *testing.T) {
	now := time.Now()
	quota := ProviderQuota{Provider: "claude", Connected: true, FiveHourRemainingPct: 80,
		WeeklyRemainingPct: 80, FiveHourResetAt: now.Add(5 * time.Minute), ObservedAt: now}
	got := EvaluateQuota(now, quota, DefaultHarvestPolicy())
	if got.Eligible || got.Reason != "inside reset safety margin" {
		t.Fatalf("unexpected eligibility: %#v", got)
	}
}

func TestChooseProviderAutoUsesBestExpiringQuota(t *testing.T) {
	now := time.Now()
	policy := DefaultHarvestPolicy()
	quotas := []ProviderQuota{
		{Provider: "codex", Connected: true, FiveHourRemainingPct: 25, WeeklyRemainingPct: 70, FiveHourResetAt: now.Add(35 * time.Minute), ObservedAt: now},
		{Provider: "claude", Connected: true, FiveHourRemainingPct: 70, WeeklyRemainingPct: 70, FiveHourResetAt: now.Add(30 * time.Minute), ObservedAt: now},
	}
	decision := ChooseProvider(now, ProviderAuto, quotas, policy)
	if decision.Provider != "claude" || !decision.Eligibility.Eligible {
		t.Fatalf("decision = %#v", decision)
	}
}
