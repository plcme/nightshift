package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

type Controller struct {
	store    *Store
	source   QuotaSource
	executor ConversationExecutor
	policy   HarvestPolicy

	mu        sync.RWMutex
	quotas    []ProviderQuota
	refreshAt time.Time
	runMu     sync.Mutex
	confirmed map[string]int
}

func NewController(store *Store, source QuotaSource, executor ConversationExecutor, policy HarvestPolicy) *Controller {
	return &Controller{store: store, source: source, executor: executor, policy: policy, confirmed: make(map[string]int)}
}

func (c *Controller) Quotas() ([]ProviderQuota, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	copyOf := append([]ProviderQuota(nil), c.quotas...)
	return copyOf, c.refreshAt
}

func (c *Controller) Refresh(ctx context.Context) []ProviderQuota {
	quotas := c.source.Read(ctx)
	c.mu.Lock()
	c.quotas = append([]ProviderQuota(nil), quotas...)
	c.refreshAt = time.Now()
	c.mu.Unlock()
	return quotas
}

func (c *Controller) RefreshAndRun(ctx context.Context) error {
	quotas := c.Refresh(ctx)
	enabledText, err := c.store.GetSetting("scheduler_enabled", "false")
	if err != nil {
		return err
	}
	enabled, _ := strconv.ParseBool(enabledText)
	if !enabled {
		return nil
	}
	return c.RunNextEligible(ctx, quotas)
}

func (c *Controller) RunNextEligible(ctx context.Context, quotas []ProviderQuota) error {
	c.runMu.Lock()
	defer c.runMu.Unlock()

	tasks, err := c.store.ListTasks(false)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Status != StatusQueued {
			continue
		}
		preference := task.ProviderPreference
		if task.SessionID != "" && task.ProviderUsed != "" {
			preference = ProviderPreference(task.ProviderUsed)
		}
		policy := c.policy
		mainWorkText, _ := c.store.GetSetting("main_work_active", "false")
		policy.MainWorkActive, _ = strconv.ParseBool(mainWorkText)
		if label, labelErr := c.store.GetLabel(task.LabelID); labelErr == nil {
			policy.MinFiveHourRemainingPct = label.MinWindowRemainingPct
			policy.WeeklyReservePct = label.WeeklyReservePct
			if task.ProviderPreference == "" || task.ProviderPreference == ProviderAuto && label.DefaultProvider != ProviderAuto {
				preference = label.DefaultProvider
			}
		}
		decision := ChooseProvider(time.Now(), preference, quotas, policy)
		if !decision.Eligibility.Eligible {
			c.clearConfirmations(task.ID)
			continue
		}
		confirmationKey := task.ID + ":" + decision.Provider
		c.confirmed[confirmationKey]++
		if c.confirmed[confirmationKey] < 2 {
			continue
		}
		c.clearConfirmations(task.ID)
		if err := validateProjectPath(task.ProjectPath); err != nil {
			_ = c.store.MarkFailed(task.ID, err.Error())
			return err
		}
		prompt, err := c.store.PendingPrompt(task.ID)
		if err != nil {
			return err
		}
		if prompt == "" {
			continue
		}
		if err := c.store.MarkRunning(task.ID, decision.Provider); err != nil {
			return err
		}
		result, err := c.executor.Execute(ctx, task, prompt, decision.Provider, decision.Eligibility.RunFor)
		if err != nil {
			_ = c.store.MarkFailed(task.ID, err.Error())
			return err
		}
		_, err = c.store.AddAssistantMessage(task.ID, result.Provider, result.SessionID, result.Output)
		return err
	}
	return nil
}

func (c *Controller) RunTaskNow(ctx context.Context, taskID string, timeout time.Duration) error {
	task, err := c.store.GetTask(taskID)
	if err != nil {
		return err
	}
	if task.Status == StatusRunning || task.Archived {
		return errors.New("task is not runnable")
	}
	if err := validateProjectPath(task.ProjectPath); err != nil {
		return err
	}
	prompt, err := c.store.PendingPrompt(task.ID)
	if err != nil {
		return err
	}
	if prompt == "" {
		return errors.New("task has no pending prompt")
	}
	provider := string(task.ProviderPreference)
	if task.ProviderUsed != "" && task.SessionID != "" {
		provider = task.ProviderUsed
	}
	if provider == string(ProviderAuto) {
		quotas, _ := c.Quotas()
		decision := ChooseProvider(time.Now(), ProviderAuto, quotas, HarvestPolicy{
			LeadTime: time.Hour * 24, SafetyMargin: 0, MinFiveHourRemainingPct: 0,
			WeeklyReservePct: c.policy.WeeklyReservePct, MaxObservationAge: time.Hour,
		})
		if decision.Provider == "" {
			return errors.New("no provider available")
		}
		provider = decision.Provider
	}
	if err := c.store.MarkRunning(task.ID, provider); err != nil {
		return err
	}
	result, err := c.executor.Execute(ctx, task, prompt, provider, timeout)
	if err != nil {
		_ = c.store.MarkFailed(task.ID, err.Error())
		return err
	}
	_, err = c.store.AddAssistantMessage(task.ID, result.Provider, result.SessionID, result.Output)
	return err
}

func (c *Controller) clearConfirmations(taskID string) {
	for key := range c.confirmed {
		if len(key) > len(taskID) && key[:len(taskID)] == taskID && key[len(taskID)] == ':' {
			delete(c.confirmed, key)
		}
	}
}

func validateProjectPath(projectPath string) error {
	if projectPath == "" {
		return errors.New("project directory is required")
	}
	info, err := os.Stat(projectPath)
	if err != nil {
		return fmt.Errorf("project directory is unavailable: %w", err)
	}
	if !info.IsDir() {
		return errors.New("project path is not a directory")
	}
	return nil
}
