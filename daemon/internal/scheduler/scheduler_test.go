package scheduler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSchedule_StandardCron(t *testing.T) {
	sched, err := ParseSchedule("0 22 * * *")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cron, ok := sched.(*StandardCronSchedule)
	if !ok {
		t.Fatalf("expected *StandardCronSchedule, got %T", sched)
	}

	testTime := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	if !cron.Matches(testTime) {
		t.Errorf("expected match at 22:00, got false")
	}

	nonMatchTime := time.Date(2026, 10, 30, 22, 1, 0, 0, time.UTC)
	if cron.Matches(nonMatchTime) {
		t.Errorf("expected no match at 22:01, got true")
	}

	fromTime := time.Date(2026, 10, 30, 10, 15, 0, 0, time.UTC)
	next := cron.Next(fromTime)
	expectedNext := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	if !next.Equal(expectedNext) {
		t.Errorf("expected next at %v, got %v", expectedNext, next)
	}
}

func TestParseSchedule_WallClockTime(t *testing.T) {
	specs := []string{"22:00", "at 22:00", "09:30"}
	for _, spec := range specs {
		sched, err := ParseSchedule(spec)
		if err != nil {
			t.Errorf("failed to parse '%s': %v", spec, err)
			continue
		}
		wc, ok := sched.(*WallClockDailySchedule)
		if !ok {
			t.Errorf("expected *WallClockDailySchedule for '%s', got %T", spec, sched)
		}
		if spec == "22:00" && (wc.hour != 22 || wc.minute != 0) {
			t.Errorf("expected 22:00, got %d:%d", wc.hour, wc.minute)
		}
	}

	sched, _ := ParseSchedule("at 22:00")
	now := time.Date(2026, 10, 30, 21, 0, 0, 0, time.UTC)
	next := sched.Next(now)
	expected := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Errorf("expected next %v, got %v", expected, next)
	}

	// If past 22:00, next should be tomorrow
	late := time.Date(2026, 10, 30, 22, 30, 0, 0, time.UTC)
	nextDay := sched.Next(late)
	expectedTomorrow := time.Date(2026, 10, 31, 22, 0, 0, 0, time.UTC)
	if !nextDay.Equal(expectedTomorrow) {
		t.Errorf("expected next %v, got %v", expectedTomorrow, nextDay)
	}
}

func TestParseSchedule_Interval(t *testing.T) {
	sched, err := ParseSchedule("every 2h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	interval, ok := sched.(*IntervalSchedule)
	if !ok {
		t.Fatalf("expected *IntervalSchedule, got %T", sched)
	}
	if interval.duration != 2*time.Hour {
		t.Errorf("expected 2h, got %v", interval.duration)
	}

	now := time.Now()
	next := interval.Next(now)
	if next.Sub(now) != 2*time.Hour {
		t.Errorf("expected 2h difference, got %v", next.Sub(now))
	}
}

func TestParseSchedule_Macros(t *testing.T) {
	macros := []string{"@hourly", "@daily", "@midnight", "@weekly", "@monthly"}
	for _, m := range macros {
		sched, err := ParseSchedule(m)
		if err != nil {
			t.Errorf("failed to parse macro '%s': %v", m, err)
		}
		if sched == nil {
			t.Errorf("nil schedule for '%s'", m)
		}
	}
}

func TestParseSchedule_Errors(t *testing.T) {
	badSpecs := []string{
		"",
		"invalid",
		"* * * *",        // 4 fields
		"* * * * * *",    // 6 fields
		"65 * * * *",     // minute out of bounds
		"* 25 * * *",     // hour out of bounds
		"every 10s",      // below 1m
		"at 25:00",       // hour out of bounds
	}
	for _, spec := range badSpecs {
		_, err := ParseSchedule(spec)
		if err == nil {
			t.Errorf("expected error for '%s', got nil", spec)
		}
	}
}

func TestScheduler_CRUDAndRun(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "unjess_sched_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	storePath := filepath.Join(tempDir, "schedules.json")

	executedTaskID := ""
	mockExecutor := TaskExecutorFunc(func(ctx context.Context, task *ScheduledTask) (string, error) {
		executedTaskID = task.ID
		if task.Action == "fail" {
			return "", errors.New("simulated execution error")
		}
		return "watchdog healthy: 0 errors", nil
	})

	s := NewScheduler(storePath, mockExecutor)

	// 1. Add Task
	task := ScheduledTask{
		Title:          "Nightly Health Watchdog",
		CronExpression: "0 22 * * *",
		Workspace:      "/path/to/repo",
		Action:         "watchdog",
		Enabled:        true,
	}

	created, err := s.AddTask(task)
	if err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}
	if created.ID == "" {
		t.Errorf("expected generated ID")
	}
	if created.NextRunAt == nil {
		t.Errorf("expected NextRunAt to be calculated")
	}

	// 2. List Tasks
	list := s.ListTasks()
	if len(list) != 1 {
		t.Fatalf("expected 1 task in list, got %d", len(list))
	}
	if list[0].Title != "Nightly Health Watchdog" {
		t.Errorf("expected title 'Nightly Health Watchdog', got '%s'", list[0].Title)
	}

	// 3. Get Task
	retrieved, exists := s.GetTask(created.ID)
	if !exists {
		t.Fatalf("task not found by ID")
	}
	if retrieved.Action != "watchdog" {
		t.Errorf("expected action 'watchdog', got '%s'", retrieved.Action)
	}

	// 4. Update Task
	updated, err := s.UpdateTask(created.ID, map[string]any{
		"title": "Nightly Code Inspector",
		"cron_expression": "every 1h",
	})
	if err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}
	if updated.Title != "Nightly Code Inspector" {
		t.Errorf("expected updated title, got '%s'", updated.Title)
	}
	if updated.CronExpression != "every 1h" {
		t.Errorf("expected updated cron expression, got '%s'", updated.CronExpression)
	}

	// 5. Toggle Task
	toggled, err := s.ToggleTask(created.ID)
	if err != nil {
		t.Fatalf("ToggleTask failed: %v", err)
	}
	if toggled.Enabled != false {
		t.Errorf("expected task to be disabled")
	}
	if toggled.NextRunAt != nil {
		t.Errorf("expected NextRunAt to be nil when disabled")
	}

	// Re-enable
	toggled2, _ := s.ToggleTask(created.ID)
	if !toggled2.Enabled {
		t.Errorf("expected task to be re-enabled")
	}
	if toggled2.NextRunAt == nil {
		t.Errorf("expected NextRunAt when enabled")
	}

	// 6. Run Task Now
	ran, err := s.RunTaskNow(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("RunTaskNow failed: %v", err)
	}
	if ran.LastStatus != "success" {
		t.Errorf("expected status 'success', got '%s'", ran.LastStatus)
	}
	if ran.LastOutput != "watchdog healthy: 0 errors" {
		t.Errorf("unexpected output: %s", ran.LastOutput)
	}
	if executedTaskID != created.ID {
		t.Errorf("expected executed task ID '%s', got '%s'", created.ID, executedTaskID)
	}

	// 7. Verify Persistence (re-instantiate scheduler)
	s2 := NewScheduler(storePath, mockExecutor)
	list2 := s2.ListTasks()
	if len(list2) != 1 {
		t.Fatalf("expected 1 task reloaded from disk, got %d", len(list2))
	}
	if list2[0].LastStatus != "success" {
		t.Errorf("expected reloaded status 'success', got '%s'", list2[0].LastStatus)
	}

	// 8. Delete Task
	deleted := s2.DeleteTask(created.ID)
	if !deleted {
		t.Errorf("expected task to be deleted")
	}
	if len(s2.ListTasks()) != 0 {
		t.Errorf("expected 0 tasks after deletion")
	}
}
