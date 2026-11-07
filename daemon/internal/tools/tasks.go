package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BackgroundTask represents a managed asynchronous process.
type BackgroundTask struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	WorkDir   string    `json:"work_dir"`
	PID       int       `json:"pid"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time,omitempty"`
	Running   bool      `json:"running"`
	ExitCode  int       `json:"exit_code"`
	Error     string    `json:"error,omitempty"`
	LogPath   string    `json:"log_path"`

	cmd    *exec.Cmd
	cancel context.CancelFunc
	mu     sync.Mutex
}

// TaskManager coordinates background tasks.
type TaskManager struct {
	tasks map[string]*BackgroundTask
	mu    sync.RWMutex
}

var (
	globalTaskManager = &TaskManager{
		tasks: make(map[string]*BackgroundTask),
	}
	taskIdCounter uint64
)

// GetGlobalTaskManager returns the shared task manager instance.
func GetGlobalTaskManager() *TaskManager {
	return globalTaskManager
}

// StartBackgroundTask launches command in the background, logging full stdout/stderr to a disk log file.
func (tm *TaskManager) StartBackgroundTask(ctx context.Context, wsPath, command, cwd string) (*BackgroundTask, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command is required")
	}

	if err := checkCommandSandbox(command, wsPath); err != nil {
		return nil, err
	}

	workDir := wsPath
	if strings.TrimSpace(cwd) != "" {
		resolved, err := resolveReadOrInspectPath(wsPath, cwd)
		if err != nil {
			return nil, fmt.Errorf("error resolving cwd: %w", err)
		}
		workDir = resolved
	}

	// Prepare logs directory
	logsDir := filepath.Join(wsPath, ".unjess", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		if home, errH := os.UserHomeDir(); errH == nil && home != "" {
			logsDir = filepath.Join(home, ".unjess", "logs")
			_ = os.MkdirAll(logsDir, 0755)
		}
	}

	num := atomic.AddUint64(&taskIdCounter, 1)
	taskID := fmt.Sprintf("task_%d", num)
	logFileName := fmt.Sprintf("task_%s_%s.log", taskID, time.Now().Format("20060102_150405"))
	logPath := filepath.Join(logsDir, logFileName)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed creating task log file: %w", err)
	}

	// Write task header
	header := fmt.Sprintf("# Task ID: %s\n# Command: %s\n# WorkDir: %s\n# Started: %s\n\n",
		taskID, command, workDir, time.Now().Format(time.RFC3339))
	_, _ = logFile.WriteString(header)

	// Context with cancel (detached from caller ctx timeout so it runs independently)
	bgCtx, cancel := context.WithCancel(context.Background())

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = buildWindowsCommand(bgCtx, command)
	} else {
		shell := "bash"
		if _, err := exec.LookPath("bash"); err != nil {
			shell = "sh"
		}
		cmd = exec.CommandContext(bgCtx, shell, "-c", command)
	}

	cmd.Dir = workDir
	setHideWindow(cmd)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "CI=true", "FORCE_COLOR=0")

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if runtime.GOOS == "windows" {
			_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		}
		return cmd.Process.Kill()
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		cancel()
		return nil, fmt.Errorf("failed starting process: %w", err)
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	task := &BackgroundTask{
		ID:        taskID,
		Command:   command,
		WorkDir:   workDir,
		PID:       pid,
		StartTime: time.Now(),
		Running:   true,
		LogPath:   logPath,
		cmd:       cmd,
		cancel:    cancel,
	}

	tm.mu.Lock()
	tm.tasks[taskID] = task
	tm.mu.Unlock()

	// Wait in background goroutine to update status and close file
	go func() {
		defer logFile.Close()
		waitErr := cmd.Wait()
		task.mu.Lock()
		task.Running = false
		task.EndTime = time.Now()
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				task.ExitCode = exitErr.ExitCode()
			} else {
				task.ExitCode = -1
				task.Error = waitErr.Error()
			}
		} else {
			task.ExitCode = 0
		}
		summary := fmt.Sprintf("\n# Process exited with code %d at %s\n", task.ExitCode, task.EndTime.Format(time.RFC3339))
		_, _ = logFile.WriteString(summary)
		task.mu.Unlock()
	}()

	return task, nil
}

// PollTask returns the current status and recent output of taskID.
func (tm *TaskManager) PollTask(taskID string) (map[string]any, error) {
	tm.mu.RLock()
	task, ok := tm.tasks[taskID]
	tm.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("task '%s' not found", taskID)
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	duration := time.Since(task.StartTime)
	if !task.Running && !task.EndTime.IsZero() {
		duration = task.EndTime.Sub(task.StartTime)
	}

	// Read output log
	logData, _ := os.ReadFile(task.LogPath)
	output := string(logData)

	// Format recent output (head/tail if long)
	lines := strings.Split(output, "\n")
	recentOutput := output
	if len(lines) > 60 {
		head := lines[:20]
		tail := lines[len(lines)-30:]
		recentOutput = strings.Join(head, "\n") +
			fmt.Sprintf("\n... [%d lines omitted. Full log: %s] ...\n", len(lines)-50, task.LogPath) +
			strings.Join(tail, "\n")
	}

	status := "running"
	if !task.Running {
		if task.ExitCode == 0 {
			status = "completed"
		} else {
			status = "failed"
		}
	}

	return map[string]any{
		"task_id":     task.ID,
		"command":     task.Command,
		"pid":         task.PID,
		"status":      status,
		"running":     task.Running,
		"exit_code":   task.ExitCode,
		"duration_s":  duration.Seconds(),
		"log_path":    task.LogPath,
		"output_tail": recentOutput,
	}, nil
}

// CancelTask terminates a running task.
func (tm *TaskManager) CancelTask(taskID string) (string, error) {
	tm.mu.RLock()
	task, ok := tm.tasks[taskID]
	tm.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("task '%s' not found", taskID)
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	if !task.Running {
		return fmt.Sprintf("Task '%s' is not running (already exited with code %d).", taskID, task.ExitCode), nil
	}

	if task.cancel != nil {
		task.cancel()
	}
	if task.cmd != nil && task.cmd.Process != nil {
		if runtime.GOOS == "windows" {
			_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(task.cmd.Process.Pid)).Run()
		} else {
			_ = task.cmd.Process.Kill()
		}
	}

	task.Running = false
	task.EndTime = time.Now()
	task.ExitCode = -9
	task.Error = "cancelled by user/agent"

	return fmt.Sprintf("Successfully cancelled task '%s' (PID %d).", taskID, task.PID), nil
}

// ReadLogTail reads the last n lines of a file efficiently.
func ReadLogTail(filePath string, maxLines int) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", err
	}

	size := stat.Size()
	if size == 0 {
		return "", nil
	}

	readBytes := int64(32768)
	if readBytes > size {
		readBytes = size
	}

	buf := make([]byte, readBytes)
	_, err = f.ReadAt(buf, size-readBytes)
	if err != nil && err != io.EOF {
		return "", err
	}

	lines := strings.Split(string(buf), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return strings.Join(lines, "\n"), nil
}
