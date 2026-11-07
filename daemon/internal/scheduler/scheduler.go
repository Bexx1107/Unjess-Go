// Package scheduler provides persistent, automated cron- and interval-based
// task execution for Unjess Daemon (unjessd).
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScheduledTask represents a persistent automated background job.
type ScheduledTask struct {
	ID             string     `json:"id"`
	Title          string     `json:"title"`
	CronExpression string     `json:"cron_expression"` // e.g. "0 22 * * *", "@daily", "every 2h", "22:00"
	Workspace      string     `json:"workspace"`       // path to target workspace directory
	Action         string     `json:"action"`          // "watchdog", "prompt", "command"
	Prompt         string     `json:"prompt"`          // instruction prompt or shell command
	Model          string     `json:"model,omitempty"` // optional model override
	Effort         string     `json:"effort,omitempty"`
	Enabled        bool       `json:"enabled"`
	NotifyOn       string     `json:"notify_on"` // "always", "on_error", "never"
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	NextRunAt      *time.Time `json:"next_run_at,omitempty"`
	LastStatus     string     `json:"last_status,omitempty"` // "success", "error", "skipped"
	LastOutput     string     `json:"last_output,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// TaskExecutor defines the callback interface for executing a scheduled task action.
type TaskExecutor interface {
	ExecuteTask(ctx context.Context, task *ScheduledTask) (output string, err error)
}

// TaskExecutorFunc allows using a plain function as a TaskExecutor.
type TaskExecutorFunc func(ctx context.Context, task *ScheduledTask) (output string, err error)

// ExecuteTask calls the underlying function.
func (f TaskExecutorFunc) ExecuteTask(ctx context.Context, task *ScheduledTask) (string, error) {
	return f(ctx, task)
}

// -----------------------------------------------------------------------------
// Cron & Spec Matching Engine
// -----------------------------------------------------------------------------

// ParsedSchedule represents a compiled schedule matcher.
type ParsedSchedule interface {
	Matches(t time.Time) bool
	Next(from time.Time) time.Time
}

// StandardCronSchedule evaluates 5-field cron expressions:
// Minute (0-59), Hour (0-23), Day of Month (1-31), Month (1-12), Day of Week (0-6, 0=Sun).
type StandardCronSchedule struct {
	minutes []int
	hours   []int
	days    []int
	months  []int
	dows    []int
	spec    string
}

// IntervalSchedule triggers repeatedly at a fixed duration.
type IntervalSchedule struct {
	duration time.Duration
	spec     string
}

// Matches returns true if the interval aligns with the given time.
func (s *IntervalSchedule) Matches(t time.Time) bool {
	return true
}

// Next calculates the next execution time based on duration from given time.
func (s *IntervalSchedule) Next(from time.Time) time.Time {
	return from.Add(s.duration)
}

// WallClockDailySchedule triggers once daily at a specific hour and minute.
type WallClockDailySchedule struct {
	hour   int
	minute int
	spec   string
}

// Matches returns true if t is at the designated hour and minute.
func (s *WallClockDailySchedule) Matches(t time.Time) bool {
	return t.Hour() == s.hour && t.Minute() == s.minute
}

// Next computes the next occurrence of this daily time.
func (s *WallClockDailySchedule) Next(from time.Time) time.Time {
	candidate := time.Date(from.Year(), from.Month(), from.Day(), s.hour, s.minute, 0, 0, from.Location())
	if !candidate.After(from) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

// ParseSchedule parses any supported schedule format:
// 1. Standard 5-field cron: "0 22 * * *", "*/15 * * * *"
// 2. Cron macros: "@hourly", "@daily", "@midnight", "@weekly", "@monthly"
// 3. Wall clock time: "22:00", "at 22:00", "09:30"
// 4. Fixed interval: "every 30m", "every 2h", "interval:1h"
func ParseSchedule(spec string) (ParsedSchedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("empty schedule specification")
	}

	lower := strings.ToLower(spec)

	// Handle macros
	switch lower {
	case "@hourly":
		return parseCronFields("0", "*", "*", "*", "*", spec)
	case "@daily", "@midnight":
		return parseCronFields("0", "0", "*", "*", "*", spec)
	case "@weekly":
		return parseCronFields("0", "0", "*", "*", "0", spec)
	case "@monthly":
		return parseCronFields("0", "0", "1", "*", "*", spec)
	}

	// Handle "every <duration>" or "interval:<duration>"
	if strings.HasPrefix(lower, "every ") || strings.HasPrefix(lower, "interval:") {
		rawDur := strings.TrimPrefix(lower, "every ")
		rawDur = strings.TrimPrefix(rawDur, "interval:")
		rawDur = strings.TrimSpace(rawDur)
		dur, err := time.ParseDuration(rawDur)
		if err != nil {
			return nil, fmt.Errorf("invalid interval duration '%s': %w", rawDur, err)
		}
		if dur < 1*time.Minute {
			return nil, errors.New("minimum scheduled interval is 1 minute")
		}
		return &IntervalSchedule{duration: dur, spec: spec}, nil
	}

	// Handle wall clock format: "HH:MM" or "at HH:MM"
	wallClockRegex := regexp.MustCompile(`^(?:at\s+)?([0-1]?[0-9]|2[0-3]):([0-5][0-9])$`)
	if match := wallClockRegex.FindStringSubmatch(lower); len(match) == 3 {
		hour, _ := strconv.Atoi(match[1])
		min, _ := strconv.Atoi(match[2])
		return &WallClockDailySchedule{hour: hour, minute: min, spec: spec}, nil
	}

	// Standard 5-field cron: min hour dom month dow
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return nil, fmt.Errorf("invalid cron expression '%s': expected 5 fields, got %d", spec, len(fields))
	}

	return parseCronFields(fields[0], fields[1], fields[2], fields[3], fields[4], spec)
}

func parseCronFields(minStr, hourStr, domStr, monStr, dowStr, spec string) (*StandardCronSchedule, error) {
	mins, err := parseCronField(minStr, 0, 59)
	if err != nil {
		return nil, fmt.Errorf("invalid minute field '%s': %w", minStr, err)
	}
	hours, err := parseCronField(hourStr, 0, 23)
	if err != nil {
		return nil, fmt.Errorf("invalid hour field '%s': %w", hourStr, err)
	}
	days, err := parseCronField(domStr, 1, 31)
	if err != nil {
		return nil, fmt.Errorf("invalid day of month field '%s': %w", domStr, err)
	}
	months, err := parseCronField(monStr, 1, 12)
	if err != nil {
		return nil, fmt.Errorf("invalid month field '%s': %w", monStr, err)
	}
	dows, err := parseCronField(dowStr, 0, 6)
	if err != nil {
		return nil, fmt.Errorf("invalid day of week field '%s': %w", dowStr, err)
	}

	return &StandardCronSchedule{
		minutes: mins,
		hours:   hours,
		days:    days,
		months:  months,
		dows:    dows,
		spec:    spec,
	}, nil
}

func parseCronField(field string, min, max int) ([]int, error) {
	if field == "*" {
		var res []int
		for i := min; i <= max; i++ {
			res = append(res, i)
		}
		return res, nil
	}

	set := make(map[int]bool)
	parts := strings.Split(field, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		step := 1
		rangeStr := part

		if strings.Contains(part, "/") {
			subParts := strings.Split(part, "/")
			if len(subParts) != 2 {
				return nil, fmt.Errorf("invalid step format in '%s'", part)
			}
			rangeStr = subParts[0]
			s, err := strconv.Atoi(subParts[1])
			if err != nil || s <= 0 {
				return nil, fmt.Errorf("invalid step value in '%s'", part)
			}
			step = s
		}

		start := min
		end := max

		if rangeStr == "*" {
			// keep full bounds
		} else if strings.Contains(rangeStr, "-") {
			rangeBounds := strings.Split(rangeStr, "-")
			if len(rangeBounds) != 2 {
				return nil, fmt.Errorf("invalid range format in '%s'", rangeStr)
			}
			b1, err1 := strconv.Atoi(rangeBounds[0])
			b2, err2 := strconv.Atoi(rangeBounds[1])
			if err1 != nil || err2 != nil || b1 < min || b2 > max || b1 > b2 {
				return nil, fmt.Errorf("invalid range bounds in '%s'", rangeStr)
			}
			start = b1
			end = b2
		} else {
			val, err := strconv.Atoi(rangeStr)
			if err != nil || val < min || val > max {
				return nil, fmt.Errorf("value '%s' out of range [%d, %d]", rangeStr, min, max)
			}
			start = val
			end = val
		}

		for i := start; i <= end; i += step {
			set[i] = true
		}
	}

	var res []int
	for i := min; i <= max; i++ {
		if set[i] {
			res = append(res, i)
		}
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("field '%s' resulted in empty value set", field)
	}
	return res, nil
}

func intSliceContains(s []int, v int) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

// Matches returns true if the time matches all 5 cron fields.
func (c *StandardCronSchedule) Matches(t time.Time) bool {
	if !intSliceContains(c.minutes, t.Minute()) {
		return false
	}
	if !intSliceContains(c.hours, t.Hour()) {
		return false
	}
	if !intSliceContains(c.days, t.Day()) {
		return false
	}
	if !intSliceContains(c.months, int(t.Month())) {
		return false
	}
	if !intSliceContains(c.dows, int(t.Weekday())) {
		return false
	}
	return true
}

// Next finds the next time strictly after 'from' that satisfies the cron specification.
// Searches up to 366 days ahead.
func (c *StandardCronSchedule) Next(from time.Time) time.Time {
	// Round up to next whole minute
	curr := from.Truncate(time.Minute).Add(time.Minute)
	limit := curr.AddDate(1, 0, 0) // Max 1 year ahead search

	for curr.Before(limit) {
		if !intSliceContains(c.months, int(curr.Month())) {
			// Advance to start of next month
			curr = time.Date(curr.Year(), curr.Month()+1, 1, 0, 0, 0, 0, curr.Location())
			continue
		}
		if !intSliceContains(c.days, curr.Day()) || !intSliceContains(c.dows, int(curr.Weekday())) {
			// Advance to start of next day
			curr = time.Date(curr.Year(), curr.Month(), curr.Day()+1, 0, 0, 0, 0, curr.Location())
			continue
		}
		if !intSliceContains(c.hours, curr.Hour()) {
			// Advance to start of next hour
			curr = time.Date(curr.Year(), curr.Month(), curr.Day(), curr.Hour()+1, 0, 0, 0, curr.Location())
			continue
		}
		if !intSliceContains(c.minutes, curr.Minute()) {
			curr = curr.Add(time.Minute)
			continue
		}
		return curr
	}

	return from.Add(24 * time.Hour)
}

// -----------------------------------------------------------------------------
// Scheduler Store & Background Engine
// -----------------------------------------------------------------------------

// Scheduler manages scheduled tasks, file persistence, and autonomous timer ticks.
type Scheduler struct {
	mu        sync.RWMutex
	storePath string
	tasks     map[string]*ScheduledTask
	executor  TaskExecutor
	cancel    context.CancelFunc
	running   bool
}

// NewScheduler constructs a new Scheduler instance.
func NewScheduler(storePath string, executor TaskExecutor) *Scheduler {
	if strings.TrimSpace(storePath) == "" {
		home, _ := os.UserHomeDir()
		storePath = filepath.Join(home, ".unjess", "schedules.json")
	}

	s := &Scheduler{
		storePath: storePath,
		tasks:     make(map[string]*ScheduledTask),
		executor:  executor,
	}
	_ = s.loadFromDisk()
	return s
}

// DefaultStorePath returns ~/.unjess/schedules.json.
func DefaultStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "schedules.json"
	}
	return filepath.Join(home, ".unjess", "schedules.json")
}

func (s *Scheduler) loadFromDisk() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var list []ScheduledTask
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}

	now := time.Now()
	for i := range list {
		task := list[i]
		// Recalculate NextRunAt if missing or past
		if task.Enabled {
			if parsed, pErr := ParseSchedule(task.CronExpression); pErr == nil {
				next := parsed.Next(now)
				task.NextRunAt = &next
			}
		}
		s.tasks[task.ID] = &task
	}
	return nil
}

func (s *Scheduler) saveToDiskLocked() error {
	var list []ScheduledTask
	for _, t := range s.tasks {
		list = append(list, *t)
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.storePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tempPath := s.storePath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tempPath, s.storePath)
}

// ListTasks returns a snapshot copy of all configured scheduled tasks.
func (s *Scheduler) ListTasks() []ScheduledTask {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res := make([]ScheduledTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		res = append(res, *t)
	}
	return res
}

// GetTask fetches a single task by its unique ID.
func (s *Scheduler) GetTask(id string) (*ScheduledTask, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task, exists := s.tasks[id]
	if !exists {
		return nil, false
	}
	cpy := *task
	return &cpy, true
}

// AddTask validates and persists a new scheduled task.
func (s *Scheduler) AddTask(task ScheduledTask) (*ScheduledTask, error) {
	if strings.TrimSpace(task.Title) == "" {
		return nil, errors.New("task title is required")
	}
	if strings.TrimSpace(task.CronExpression) == "" {
		return nil, errors.New("cron_expression is required")
	}

	parsed, err := ParseSchedule(task.CronExpression)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule format: %w", err)
	}

	if task.ID == "" {
		task.ID = fmt.Sprintf("sched_%d", time.Now().UnixNano())
	}
	if task.Action == "" {
		task.Action = "watchdog"
	}
	if task.NotifyOn == "" {
		task.NotifyOn = "on_error"
	}

	now := time.Now()
	task.CreatedAt = now
	if task.Enabled {
		next := parsed.Next(now)
		task.NextRunAt = &next
	} else {
		task.NextRunAt = nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.tasks[task.ID] = &task
	if err := s.saveToDiskLocked(); err != nil {
		return nil, fmt.Errorf("failed to persist schedule: %w", err)
	}

	cpy := task
	return &cpy, nil
}

// UpdateTask modifies properties of an existing scheduled task.
func (s *Scheduler) UpdateTask(id string, updates map[string]any) (*ScheduledTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, exists := s.tasks[id]
	if !exists {
		return nil, fmt.Errorf("task with id '%s' not found", id)
	}

	if title, ok := updates["title"].(string); ok && strings.TrimSpace(title) != "" {
		task.Title = strings.TrimSpace(title)
	}
	if ws, ok := updates["workspace"].(string); ok {
		task.Workspace = strings.TrimSpace(ws)
	}
	if action, ok := updates["action"].(string); ok && strings.TrimSpace(action) != "" {
		task.Action = strings.TrimSpace(action)
	}
	if prompt, ok := updates["prompt"].(string); ok {
		task.Prompt = prompt
	}
	if model, ok := updates["model"].(string); ok {
		task.Model = model
	}
	if effort, ok := updates["effort"].(string); ok {
		task.Effort = effort
	}
	if notify, ok := updates["notify_on"].(string); ok && strings.TrimSpace(notify) != "" {
		task.NotifyOn = strings.TrimSpace(notify)
	}
	if enabled, ok := updates["enabled"].(bool); ok {
		task.Enabled = enabled
	}

	if cronExpr, ok := updates["cron_expression"].(string); ok && strings.TrimSpace(cronExpr) != "" {
		parsed, err := ParseSchedule(cronExpr)
		if err != nil {
			return nil, fmt.Errorf("invalid schedule format: %w", err)
		}
		task.CronExpression = strings.TrimSpace(cronExpr)
		if task.Enabled {
			next := parsed.Next(time.Now())
			task.NextRunAt = &next
		}
	} else if task.Enabled {
		if parsed, err := ParseSchedule(task.CronExpression); err == nil {
			next := parsed.Next(time.Now())
			task.NextRunAt = &next
		}
	} else {
		task.NextRunAt = nil
	}

	if err := s.saveToDiskLocked(); err != nil {
		return nil, fmt.Errorf("failed to save task updates: %w", err)
	}

	cpy := *task
	return &cpy, nil
}

// DeleteTask removes a scheduled task by ID.
func (s *Scheduler) DeleteTask(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[id]; !exists {
		return false
	}
	delete(s.tasks, id)
	_ = s.saveToDiskLocked()
	return true
}

// ToggleTask flips the Enabled boolean of a task.
func (s *Scheduler) ToggleTask(id string) (*ScheduledTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, exists := s.tasks[id]
	if !exists {
		return nil, fmt.Errorf("task with id '%s' not found", id)
	}

	task.Enabled = !task.Enabled
	if task.Enabled {
		if parsed, err := ParseSchedule(task.CronExpression); err == nil {
			next := parsed.Next(time.Now())
			task.NextRunAt = &next
		}
	} else {
		task.NextRunAt = nil
	}

	_ = s.saveToDiskLocked()
	cpy := *task
	return &cpy, nil
}

// RunTaskNow manually executes a task immediately out of band, updating its status.
func (s *Scheduler) RunTaskNow(ctx context.Context, id string) (*ScheduledTask, error) {
	s.mu.RLock()
	task, exists := s.tasks[id]
	if !exists {
		s.mu.RUnlock()
		return nil, fmt.Errorf("task with id '%s' not found", id)
	}
	taskCopy := *task
	s.mu.RUnlock()

	now := time.Now()
	var out string
	var execErr error

	if s.executor != nil {
		out, execErr = s.executor.ExecuteTask(ctx, &taskCopy)
	} else {
		out = "No executor attached to scheduler engine"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	currTask, exists := s.tasks[id]
	if !exists {
		return nil, fmt.Errorf("task with id '%s' deleted during execution", id)
	}

	currTask.LastRunAt = &now
	if execErr != nil {
		currTask.LastStatus = "error"
		currTask.LastOutput = fmt.Sprintf("Error: %v\nOutput: %s", execErr, out)
	} else {
		currTask.LastStatus = "success"
		currTask.LastOutput = out
	}

	// Calculate next run
	if currTask.Enabled {
		if parsed, err := ParseSchedule(currTask.CronExpression); err == nil {
			next := parsed.Next(now)
			currTask.NextRunAt = &next
		}
	}

	_ = s.saveToDiskLocked()
	finalCopy := *currTask
	return &finalCopy, execErr
}

// Start launches the background scheduler ticker.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()

	go s.runLoop(ctx)
}

// Stop terminates the background ticker gracefully.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
}

func (s *Scheduler) runLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.checkAndTriggerPendingTasks(ctx, now)
		}
	}
}

func (s *Scheduler) checkAndTriggerPendingTasks(ctx context.Context, now time.Time) {
	s.mu.RLock()
	var dueTasks []*ScheduledTask
	for _, t := range s.tasks {
		if t.Enabled && t.NextRunAt != nil && !t.NextRunAt.After(now) {
			cpy := *t
			dueTasks = append(dueTasks, &cpy)
		}
	}
	s.mu.RUnlock()

	for _, task := range dueTasks {
		// Run task asynchronously so one slow task doesn't block the loop
		go func(t *ScheduledTask) {
			execCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			_, _ = s.RunTaskNow(execCtx, t.ID)
		}(task)
	}
}
