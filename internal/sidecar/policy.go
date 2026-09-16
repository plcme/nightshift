package sidecar

import (
	"fmt"
	"sort"
	"time"
)

type ProviderQuota struct {
	Provider             string    `json:"provider"`
	Connected            bool      `json:"connected"`
	FiveHourRemainingPct float64   `json:"five_hour_remaining_pct"`
	FiveHourResetAt      time.Time `json:"five_hour_reset_at"`
	WeeklyRemainingPct   float64   `json:"weekly_remaining_pct"`
	ObservedAt           time.Time `json:"observed_at"`
	Error                string    `json:"error,omitempty"`
}

type HarvestPolicy struct {
	LeadTime                 time.Duration `json:"lead_time"`
	SafetyMargin             time.Duration `json:"safety_margin"`
	MinFiveHourRemainingPct  float64       `json:"min_five_hour_remaining_pct"`
	WeeklyReservePct         float64       `json:"weekly_reserve_pct"`
	MaxObservationAge        time.Duration `json:"max_observation_age"`
	MainWorkActive           bool          `json:"main_work_active"`
	AllowWhileMainWorkActive bool          `json:"allow_while_main_work_active"`
}

func DefaultHarvestPolicy() HarvestPolicy {
	return HarvestPolicy{
		LeadTime:                45 * time.Minute,
		SafetyMargin:            8 * time.Minute,
		MinFiveHourRemainingPct: 10,
		WeeklyReservePct:        20,
		MaxObservationAge:       10 * time.Minute,
	}
}

type Eligibility struct {
	Eligible bool          `json:"eligible"`
	Reason   string        `json:"reason"`
	RunFor   time.Duration `json:"run_for"`
}

func EvaluateQuota(now time.Time, quota ProviderQuota, policy HarvestPolicy) Eligibility {
	if !quota.Connected {
		return Eligibility{Reason: "provider disconnected"}
	}
	if policy.MainWorkActive && !policy.AllowWhileMainWorkActive {
		return Eligibility{Reason: "main work active"}
	}
	if quota.ObservedAt.IsZero() || now.Sub(quota.ObservedAt) > policy.MaxObservationAge {
		return Eligibility{Reason: "quota observation stale"}
	}
	if quota.FiveHourResetAt.IsZero() {
		return Eligibility{Reason: "5h reset unavailable"}
	}
	untilReset := quota.FiveHourResetAt.Sub(now)
	if untilReset > policy.LeadTime {
		return Eligibility{Reason: fmt.Sprintf("harvest window opens in %s", (untilReset - policy.LeadTime).Round(time.Minute))}
	}
	if untilReset <= policy.SafetyMargin {
		return Eligibility{Reason: "inside reset safety margin"}
	}
	if quota.FiveHourRemainingPct < policy.MinFiveHourRemainingPct {
		return Eligibility{Reason: "not enough 5h quota remaining"}
	}
	if quota.WeeklyRemainingPct < policy.WeeklyReservePct {
		return Eligibility{Reason: "weekly reserve protected"}
	}
	return Eligibility{Eligible: true, Reason: "unused quota near reset", RunFor: untilReset - policy.SafetyMargin}
}

type ProviderDecision struct {
	Provider    string      `json:"provider"`
	Eligibility Eligibility `json:"eligibility"`
}

func ChooseProvider(now time.Time, preference ProviderPreference, quotas []ProviderQuota, policy HarvestPolicy) ProviderDecision {
	allowed := func(provider string) bool {
		return preference == ProviderAuto || string(preference) == provider
	}
	type candidate struct {
		quota       ProviderQuota
		eligibility Eligibility
		score       float64
	}
	var candidates []candidate
	var firstRejected ProviderDecision
	for _, quota := range quotas {
		if !allowed(quota.Provider) {
			continue
		}
		eligibility := EvaluateQuota(now, quota, policy)
		if firstRejected.Provider == "" {
			firstRejected = ProviderDecision{Provider: quota.Provider, Eligibility: eligibility}
		}
		if !eligibility.Eligible {
			continue
		}
		minutes := quota.FiveHourResetAt.Sub(now).Minutes()
		urgency := 1.0
		if policy.LeadTime > 0 {
			urgency += 1 - minutes/policy.LeadTime.Minutes()
		}
		candidates = append(candidates, candidate{quota: quota, eligibility: eligibility,
			score: quota.FiveHourRemainingPct * urgency})
	}
	if len(candidates) == 0 {
		if firstRejected.Provider == "" {
			return ProviderDecision{Eligibility: Eligibility{Reason: "no configured provider"}}
		}
		return firstRejected
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	return ProviderDecision{Provider: candidates[0].quota.Provider, Eligibility: candidates[0].eligibility}
}
