package frontier

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func setupProcessTreeKill(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.WaitDelay = 1500 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if runtime.GOOS == "windows" {
			_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		}
		return cmd.Process.Kill()
	}
}

// -----------------------------------------------------------------------------
// #9 Self-Evolving Tool Synthesis
// -----------------------------------------------------------------------------

// CustomTool represents a dynamically synthesized or user-defined custom tool.
type CustomTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Language    string         `json:"language"` // "python", "powershell", "node", "go"
	Code        string         `json:"code"`
	ScriptPath  string         `json:"script_path"`
	Parameters  map[string]any `json:"parameters"`
	CreatedAt   string         `json:"created_at"`
}

var (
	customToolsMu sync.RWMutex
	toolNameRegex = regexp.MustCompile(`[^a-z0-9_]`)
)

// GetCustomToolsDir returns the base directory for storing custom tools.
// Can be overridden via UNJESS_CUSTOM_TOOLS_DIR environment variable.
func GetCustomToolsDir() string {
	if d := os.Getenv("UNJESS_CUSTOM_TOOLS_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".unjess", "custom_tools")
	}
	return filepath.Join(home, ".unjess", "custom_tools")
}

func getToolsJSONPath() string {
	return filepath.Join(GetCustomToolsDir(), "tools.json")
}

func sanitizeToolName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, "-", "_")
	s = toolNameRegex.ReplaceAllString(s, "")
	if s == "" {
		s = "tool"
	}
	if !strings.HasPrefix(s, "custom_") {
		s = "custom_" + s
	}
	return s
}

// SynthesizeTool generates, compiles (if Go), and persists a new custom tool.
func SynthesizeTool(name, description, language, code string, parameters map[string]any) (*CustomTool, error) {
	customToolsMu.Lock()
	defer customToolsMu.Unlock()

	sanitizedName := sanitizeToolName(name)
	lang := strings.ToLower(strings.TrimSpace(language))
	ext := ".sh"
	switch lang {
	case "python", "py":
		lang = "python"
		ext = ".py"
	case "powershell", "ps1", "posh":
		lang = "powershell"
		ext = ".ps1"
	case "node", "js", "javascript":
		lang = "node"
		ext = ".js"
	case "go", "golang":
		lang = "go"
		ext = ".go"
	default:
		if lang == "" {
			lang = "python"
			ext = ".py"
		}
	}

	baseDir := GetCustomToolsDir()
	binDir := filepath.Join(baseDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create custom tools bin directory: %w", err)
	}

	scriptPath := filepath.Join(binDir, sanitizedName+ext)
	if err := os.WriteFile(scriptPath, []byte(code), 0755); err != nil {
		return nil, fmt.Errorf("failed to write tool script: %w", err)
	}

	// If Go, compile it to a native binary for maximum execution speed
	if lang == "go" {
		exeName := sanitizedName
		if runtime.GOOS == "windows" {
			exeName += ".exe"
		}
		binPath := filepath.Join(binDir, exeName)
		buildCtx, buildCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer buildCancel()

		cmd := exec.CommandContext(buildCtx, "go", "build", "-o", binPath, scriptPath)
		cmd.Dir = binDir
		setupProcessTreeKill(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("failed to compile go tool (%s): %w (output: %s)", scriptPath, err, string(out))
		}
		scriptPath = binPath
	}

	if parameters == nil {
		parameters = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	tool := CustomTool{
		Name:        sanitizedName,
		Description: description,
		Language:    lang,
		Code:        code,
		ScriptPath:  scriptPath,
		Parameters:  parameters,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	tools, _ := loadCustomToolsLocked()
	updated := false
	for i, t := range tools {
		if t.Name == sanitizedName {
			tools[i] = tool
			updated = true
			break
		}
	}
	if !updated {
		tools = append(tools, tool)
	}

	if err := saveCustomToolsLocked(tools); err != nil {
		return nil, fmt.Errorf("failed to save tools.json: %w", err)
	}

	return &tool, nil
}

// LoadCustomTools retrieves all registered custom tools from tools.json.
func LoadCustomTools() ([]CustomTool, error) {
	customToolsMu.RLock()
	defer customToolsMu.RUnlock()
	return loadCustomToolsLocked()
}

func loadCustomToolsLocked() ([]CustomTool, error) {
	path := getToolsJSONPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []CustomTool{}, nil
		}
		return nil, err
	}
	var tools []CustomTool
	if err := json.Unmarshal(data, &tools); err != nil {
		return []CustomTool{}, nil
	}
	return tools, nil
}

func saveCustomToolsLocked(tools []CustomTool) error {
	path := getToolsJSONPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// DeleteCustomTool removes a tool from tools.json and deletes its script or binary file.
func DeleteCustomTool(name string) error {
	customToolsMu.Lock()
	defer customToolsMu.Unlock()

	sanitized := sanitizeToolName(name)
	tools, err := loadCustomToolsLocked()
	if err != nil {
		return err
	}

	var filtered []CustomTool
	var foundTool *CustomTool
	for _, t := range tools {
		if t.Name == sanitized || t.Name == name {
			toolCopy := t
			foundTool = &toolCopy
		} else {
			filtered = append(filtered, t)
		}
	}

	if foundTool != nil && foundTool.ScriptPath != "" {
		_ = os.Remove(foundTool.ScriptPath)
		if strings.HasSuffix(foundTool.ScriptPath, ".exe") {
			_ = os.Remove(strings.TrimSuffix(foundTool.ScriptPath, ".exe") + ".go")
		}
	}

	return saveCustomToolsLocked(filtered)
}

// ExecuteCustomTool runs a custom tool by name with arguments and returns (output, found, err).
func ExecuteCustomTool(ctx context.Context, wsPath, name string, args map[string]any) (string, bool, error) {
	tools, err := LoadCustomTools()
	if err != nil {
		return "", false, err
	}

	sanitized := sanitizeToolName(name)
	var target *CustomTool
	for _, t := range tools {
		if t.Name == sanitized || t.Name == name {
			toolCopy := t
			target = &toolCopy
			break
		}
	}

	if target == nil {
		return "", false, nil
	}

	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", true, fmt.Errorf("failed to marshal tool args: %w", err)
	}

	var cmd *exec.Cmd
	switch target.Language {
	case "python":
		pyExec := "python"
		if runtime.GOOS != "windows" {
			if _, err := exec.LookPath("python3"); err == nil {
				pyExec = "python3"
			}
		}
		cmd = exec.CommandContext(execCtx, pyExec, target.ScriptPath)
	case "powershell":
		psExec := "powershell"
		if runtime.GOOS != "windows" {
			if _, err := exec.LookPath("pwsh"); err == nil {
				psExec = "pwsh"
			}
		}
		cmd = exec.CommandContext(execCtx, psExec, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", target.ScriptPath)
	case "node":
		cmd = exec.CommandContext(execCtx, "node", target.ScriptPath)
	case "go":
		if strings.HasSuffix(target.ScriptPath, ".exe") || !strings.HasSuffix(target.ScriptPath, ".go") {
			cmd = exec.CommandContext(execCtx, target.ScriptPath)
		} else {
			cmd = exec.CommandContext(execCtx, "go", "run", target.ScriptPath)
		}
	default:
		cmd = exec.CommandContext(execCtx, target.ScriptPath)
	}

	if wsPath != "" {
		cmd.Dir = wsPath
	}
	setupProcessTreeKill(cmd)
	cmd.Env = append(os.Environ(), "UNJESS_TOOL_ARGS="+string(argsJSON))
	cmd.Stdin = bytes.NewReader(argsJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	execErr := cmd.Run()
	outStr := strings.TrimSpace(stdout.String())
	errStr := strings.TrimSpace(stderr.String())

	var combined string
	if outStr != "" && errStr != "" {
		combined = outStr + "\n" + errStr
	} else if outStr != "" {
		combined = outStr
	} else {
		combined = errStr
	}

	return combined, true, execErr
}

// CustomToolDefinitions returns standard function-calling definitions for all registered custom tools.
func CustomToolDefinitions() []map[string]any {
	tools, err := LoadCustomTools()
	if err != nil {
		return []map[string]any{}
	}

	defs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		}
		defs = append(defs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			},
		})
	}
	return defs
}

// -----------------------------------------------------------------------------
// #2 Dynamic Intra-Turn Context Compaction & Token Pruning
// -----------------------------------------------------------------------------

// CompactOldToolOutput compacts lengthy tool outputs into head + tail summaries.
func CompactOldToolOutput(toolName string, content string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 1200
	}
	if len(content) <= maxLen {
		return content
	}

	headLen := 500
	tailLen := 300
	if len(content) <= headLen+tailLen {
		return content
	}

	head := content[:headLen]
	tail := content[len(content)-tailLen:]

	return fmt.Sprintf("[Compacted earlier %s output (%d chars):\n%s\n... [pruned to save context window] ...\n%s]",
		toolName, len(content), head, tail)
}

// PruneIntraTurnToolOutputs compacts older tool outputs across a conversation history.
func PruneIntraTurnToolOutputs(messagesTotalChars int, maxToolOutputChars int, keepRecentTools int, messages []map[string]any) []map[string]any {
	if len(messages) == 0 {
		return messages
	}
	if maxToolOutputChars <= 0 {
		maxToolOutputChars = 1200
	}
	if keepRecentTools < 0 {
		keepRecentTools = 2
	}

	totalChars := 0
	for _, msg := range messages {
		if content, ok := msg["content"].(string); ok {
			totalChars += len(content)
		}
	}
	if messagesTotalChars > 0 && totalChars <= messagesTotalChars {
		return messages
	}

	type toolRef struct {
		idx  int
		name string
	}
	var toolIndices []toolRef
	for i, msg := range messages {
		role, _ := msg["role"].(string)
		name, _ := msg["name"].(string)
		if name == "" {
			name, _ = msg["tool_name"].(string)
		}
		if role == "tool" || name != "" {
			if name == "" {
				name = "tool"
			}
			toolIndices = append(toolIndices, toolRef{idx: i, name: name})
		}
	}

	compactCount := len(toolIndices) - keepRecentTools
	if compactCount <= 0 {
		return messages
	}

	cloned := make([]map[string]any, len(messages))
	for i, msg := range messages {
		m := make(map[string]any, len(msg))
		for k, v := range msg {
			m[k] = v
		}
		cloned[i] = m
	}

	for k := 0; k < compactCount; k++ {
		idx := toolIndices[k].idx
		name := toolIndices[k].name
		if content, ok := cloned[idx]["content"].(string); ok {
			if len(content) > maxToolOutputChars {
				cloned[idx]["content"] = CompactOldToolOutput(name, content, maxToolOutputChars)
			}
		}
	}

	return cloned
}

// BuildRollingCompactionPrompt generates an executive summary prompt for /compact.
func BuildRollingCompactionPrompt(turnsSummary string) string {
	return "Summarize the key context, architectural decisions made, files inspected or modified, and current progress from these turns into a concise executive summary to free up the conversation context window while preserving critical technical details and pending goals:\n\n" + turnsSummary
}

// -----------------------------------------------------------------------------
// #13 Dynamic Model Cascade Router
// -----------------------------------------------------------------------------

// RouteDecision represents the model routing selection.
type RouteDecision struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Tier     string `json:"tier"` // "fast", "balanced", "frontier"
	Reason   string `json:"reason"`
}

func inferProvider(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "chatgpt"):
		return "openai"
	case strings.HasPrefix(m, "claude-"):
		return "anthropic"
	case strings.HasPrefix(m, "gemini-"):
		return "google"
	case strings.HasPrefix(m, "deepseek-"):
		return "deepseek"
	case strings.HasPrefix(m, "ollama:") || strings.Contains(m, "llama") || strings.Contains(m, "mistral") || strings.Contains(m, "qwen"):
		return "ollama"
	case strings.Contains(m, "/"):
		parts := strings.SplitN(m, "/", 2)
		return parts[0]
	default:
		return "auto"
	}
}

// SelectModelForTask chooses an appropriate model tier based on task kind and prompt cues.
func SelectModelForTask(taskKind string, prompt string, primaryModel string, fastModel string) RouteDecision {
	normalizedKind := strings.ToLower(strings.TrimSpace(taskKind))
	lowerPrompt := strings.ToLower(prompt)

	if normalizedKind == "" {
		switch {
		case strings.Contains(lowerPrompt, "explore") || strings.Contains(lowerPrompt, "quick scan") || strings.Contains(lowerPrompt, "find files") || strings.Contains(lowerPrompt, "search") || strings.Contains(lowerPrompt, "list files"):
			normalizedKind = "explore"
		case strings.Contains(lowerPrompt, "summarize") || strings.Contains(lowerPrompt, "recap") || strings.Contains(lowerPrompt, "compact") || strings.Contains(lowerPrompt, "overview"):
			normalizedKind = "summarize"
		case strings.Contains(lowerPrompt, "architect") || strings.Contains(lowerPrompt, "system design") || strings.Contains(lowerPrompt, "blueprint"):
			normalizedKind = "architect"
		case strings.Contains(lowerPrompt, "security") || strings.Contains(lowerPrompt, "duel") || strings.Contains(lowerPrompt, "vulnerability") || strings.Contains(lowerPrompt, "exploit"):
			normalizedKind = "security_duel"
		case strings.Contains(lowerPrompt, "critic") || strings.Contains(lowerPrompt, "review") || strings.Contains(lowerPrompt, "audit"):
			normalizedKind = "critic"
		default:
			normalizedKind = "implement"
		}
	}

	if (normalizedKind == "explore" || normalizedKind == "summarize") && fastModel != "" {
		return RouteDecision{
			Model:    fastModel,
			Provider: inferProvider(fastModel),
			Tier:     "fast",
			Reason:   fmt.Sprintf("Low latency fast model selected for %s task.", normalizedKind),
		}
	}

	return RouteDecision{
		Model:    primaryModel,
		Provider: inferProvider(primaryModel),
		Tier:     "frontier",
		Reason:   fmt.Sprintf("Frontier high-reasoning model required for %s task.", normalizedKind),
	}
}

// RouteModelForStep is an alias for SelectModelForTask.
func RouteModelForStep(taskKind string, prompt string, primaryModel string, fastModel string) RouteDecision {
	return SelectModelForTask(taskKind, prompt, primaryModel, fastModel)
}

// -----------------------------------------------------------------------------
// #10 Multimodal Visual QA Loop
// -----------------------------------------------------------------------------

// IsSupportedImageExtension returns true if the file extension is an image format supported for Vision inspection.
func IsSupportedImageExtension(filePath string) bool {
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(filePath)))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp":
		return true
	default:
		return false
	}
}

func findAllHeadlessBrowsers() []string {
	var rawCandidates []string

	// Prioritize Chrome/Chromium first (faster headless startup and avoids Windows 11 Edge Startup Boost / WebView2 contention)
	pathNames := []string{"chrome", "google-chrome", "chromium", "chromium-browser", "brave", "msedge"}
	for _, name := range pathNames {
		if p, err := exec.LookPath(name); err == nil {
			rawCandidates = append(rawCandidates, p)
		}
	}

	if runtime.GOOS == "windows" {
		rawCandidates = append(rawCandidates,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		)
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			rawCandidates = append(rawCandidates,
				filepath.Join(localAppData, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(localAppData, `BraveSoftware\Brave-Browser\Application\brave.exe`),
			)
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			rawCandidates = append(rawCandidates,
				filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(pf, `BraveSoftware\Brave-Browser\Application\brave.exe`),
			)
		}
		if pfx86 := os.Getenv("ProgramFiles(x86)"); pfx86 != "" {
			rawCandidates = append(rawCandidates,
				filepath.Join(pfx86, `Google\Chrome\Application\chrome.exe`),
			)
		}
		// Fallback to Edge after Chrome/Brave
		rawCandidates = append(rawCandidates,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		)
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			rawCandidates = append(rawCandidates,
				filepath.Join(localAppData, `Microsoft\Edge\Application\msedge.exe`),
			)
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			rawCandidates = append(rawCandidates,
				filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
			)
		}
		if pfx86 := os.Getenv("ProgramFiles(x86)"); pfx86 != "" {
			rawCandidates = append(rawCandidates,
				filepath.Join(pfx86, `Microsoft\Edge\Application\msedge.exe`),
			)
		}
	} else {
		rawCandidates = append(rawCandidates,
			"/usr/bin/google-chrome",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		)
	}

	seen := make(map[string]bool)
	var available []string
	for _, c := range rawCandidates {
		norm := strings.ToLower(filepath.Clean(c))
		if seen[norm] {
			continue
		}
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			seen[norm] = true
			available = append(available, c)
		}
	}
	return available
}

func findHeadlessBrowser() string {
	browsers := findAllHeadlessBrowsers()
	if len(browsers) > 0 {
		return browsers[0]
	}
	return ""
}

// resizeImageBilinear downscales src to newW x newH using bilinear interpolation onto an opaque white/dark background.
func resizeImageBilinear(src image.Image, newW, newH int) *image.RGBA {
	b := src.Bounds()
	srcW := b.Dx()
	srcH := b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	if srcW <= 0 || srcH <= 0 || newW <= 0 || newH <= 0 {
		return dst
	}

	xRatio := float64(srcW) / float64(newW)
	yRatio := float64(srcH) / float64(newH)

	for y := 0; y < newH; y++ {
		sy := (float64(y)+0.5)*yRatio - 0.5
		if sy < 0 {
			sy = 0
		}
		y0 := int(sy)
		y1 := y0 + 1
		if y1 >= srcH {
			y1 = srcH - 1
		}
		wy := sy - float64(y0)

		for x := 0; x < newW; x++ {
			sx := (float64(x)+0.5)*xRatio - 0.5
			if sx < 0 {
				sx = 0
			}
			x0 := int(sx)
			x1 := x0 + 1
			if x1 >= srcW {
				x1 = srcW - 1
			}
			wx := sx - float64(x0)

			r00, g00, b00, a00 := src.At(b.Min.X+x0, b.Min.Y+y0).RGBA()
			r10, g10, b10, a10 := src.At(b.Min.X+x1, b.Min.Y+y0).RGBA()
			r01, g01, b01, a01 := src.At(b.Min.X+x0, b.Min.Y+y1).RGBA()
			r11, g11, b11, a11 := src.At(b.Min.X+x1, b.Min.Y+y1).RGBA()

			w00 := (1 - wx) * (1 - wy)
			w10 := wx * (1 - wy)
			w01 := (1 - wx) * wy
			w11 := wx * wy

			rf := (float64(r00)*w00 + float64(r10)*w10 + float64(r01)*w01 + float64(r11)*w11) / 257.0
			gf := (float64(g00)*w00 + float64(g10)*w10 + float64(g01)*w01 + float64(g11)*w11) / 257.0
			bf := (float64(b00)*w00 + float64(b10)*w10 + float64(b01)*w01 + float64(b11)*w11) / 257.0
			af := (float64(a00)*w00 + float64(a10)*w10 + float64(a01)*w01 + float64(a11)*w11) / 257.0

			if af < 255 {
				// Composite translucent pixels over dark neutral background (#111115) for JPEG encoding
				alpha := af / 255.0
				rf = rf*alpha + 17.0*(1.0-alpha)
				gf = gf*alpha + 17.0*(1.0-alpha)
				bf = bf*alpha + 21.0*(1.0-alpha)
			}

			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(clampByte(rf)),
				G: uint8(clampByte(gf)),
				B: uint8(clampByte(bf)),
				A: 255,
			})
		}
	}
	return dst
}

func clampByte(v float64) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return int(v + 0.5)
}

// LoadAndOptimizeImageForVision loads an image file from disk, automatically downscales high-resolution
// images (e.g. 4K / 2752x1536 / >450KB) to <= maxDim (default 1568px) JPEG at 85% quality so Vision LLMs
// (Ollama Cloud, Anthropic, Gemini, OpenAI, Qwen) receive compact, fast, standards-compliant base64 payloads.
func LoadAndOptimizeImageForVision(filePath string, maxDim int) (dataURI string, mimeType string, width int, height int, origBytes int, optBytes int, err error) {
	if maxDim <= 0 {
		maxDim = 1568
	}

	rawData, readErr := os.ReadFile(filePath)
	if readErr != nil {
		return "", "", 0, 0, 0, 0, fmt.Errorf("failed to read image file %s: %w", filePath, readErr)
	}
	if len(rawData) == 0 {
		return "", "", 0, 0, 0, 0, fmt.Errorf("image file %s is empty (0 bytes)", filePath)
	}
	origBytes = len(rawData)

	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".webp":
		mimeType = "image/webp"
	case ".gif":
		mimeType = "image/gif"
	case ".bmp":
		mimeType = "image/bmp"
	default:
		mimeType = "image/jpeg"
	}

	// Attempt to decode via standard library (supports jpeg, png, gif)
	img, format, decErr := image.Decode(bytes.NewReader(rawData))
	if decErr == nil && img != nil {
		b := img.Bounds()
		w := b.Dx()
		h := b.Dy()
		if w > 0 && h > 0 {
			switch format {
			case "png":
				mimeType = "image/png"
			case "jpeg":
				mimeType = "image/jpeg"
			case "gif":
				mimeType = "image/gif"
			}

			// Downscale or re-encode as compact JPEG if dimensions exceed maxDim or raw size > 450 KB
			if w > maxDim || h > maxDim || origBytes > 450*1024 {
				newW := w
				newH := h
				if w > maxDim || h > maxDim {
					if w >= h {
						newW = maxDim
						newH = int(float64(h) * float64(maxDim) / float64(w))
					} else {
						newH = maxDim
						newW = int(float64(w) * float64(maxDim) / float64(h))
					}
					if newW < 1 {
						newW = 1
					}
					if newH < 1 {
						newH = 1
					}
				}
				resized := resizeImageBilinear(img, newW, newH)
				var jpgBuf bytes.Buffer
				if encErr := jpeg.Encode(&jpgBuf, resized, &jpeg.Options{Quality: 85}); encErr == nil && jpgBuf.Len() > 0 {
					b64 := base64.StdEncoding.EncodeToString(jpgBuf.Bytes())
					return "data:image/jpeg;base64," + b64, "image/jpeg", newW, newH, origBytes, jpgBuf.Len(), nil
				}
				var pngBuf bytes.Buffer
				if encErr := png.Encode(&pngBuf, resized); encErr == nil && pngBuf.Len() > 0 {
					b64 := base64.StdEncoding.EncodeToString(pngBuf.Bytes())
					return "data:image/png;base64," + b64, "image/png", newW, newH, origBytes, pngBuf.Len(), nil
				}
			}

			// Image is already compact and within maxDim
			b64 := base64.StdEncoding.EncodeToString(rawData)
			return fmt.Sprintf("data:%s;base64,%s", mimeType, b64), mimeType, w, h, origBytes, origBytes, nil
		}
	}

	// Fallback for formats not decoded by stdlib (e.g., .webp): return raw base64
	b64 := base64.StdEncoding.EncodeToString(rawData)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, b64), mimeType, 0, 0, origBytes, origBytes, nil
}

// CaptureVisualQA captures a rendered screenshot of a web page or local HTML file via headless browser,
// or loads and optimizes an existing local image file (.png, .jpg, .jpeg, .webp, .gif) directly into Vision input.
func CaptureVisualQA(ctx context.Context, wsPath, targetURLOrFile string, width, height int) (base64DataURI string, metaReport string, err error) {
	if width <= 0 {
		width = 1280
	}
	if height <= 0 {
		height = 800
	}

	// Resolve local path if not an http/https URL
	trimmedTarget := strings.TrimSpace(targetURLOrFile)
	isHTTP := strings.HasPrefix(trimmedTarget, "http://") || strings.HasPrefix(trimmedTarget, "https://")
	var localFullPath string
	if !isHTTP {
		rawLocal := trimmedTarget
		if strings.HasPrefix(rawLocal, "file:///") {
			rawLocal = strings.TrimPrefix(rawLocal, "file:///")
		} else if strings.HasPrefix(rawLocal, "file://") {
			rawLocal = strings.TrimPrefix(rawLocal, "file://")
		}
		if filepath.IsAbs(rawLocal) {
			localFullPath = rawLocal
		} else if wsPath != "" {
			localFullPath = filepath.Join(wsPath, rawLocal)
		} else {
			localFullPath, _ = filepath.Abs(rawLocal)
		}

		// If the target is a local image file (.png, .jpg, .jpeg, .webp, .gif, .bmp), load & optimize it directly!
		if IsSupportedImageExtension(localFullPath) {
			dataURI, mimeType, imgW, imgH, origSz, optSz, loadErr := LoadAndOptimizeImageForVision(localFullPath, 1568)
			if loadErr != nil {
				return "", "", loadErr
			}
			dimInfo := ""
			if imgW > 0 && imgH > 0 {
				dimInfo = fmt.Sprintf(", %dx%d px", imgW, imgH)
			}
			metaReport = fmt.Sprintf("Image loaded from disk and attached to your Vision input (%s%s, %d KB -> %d KB optimized): %s — you can now see and inspect this image directly.",
				mimeType, dimInfo, origSz/1024, optSz/1024, localFullPath)
			return dataURI, metaReport, nil
		}
	}

	browsers := findAllHeadlessBrowsers()
	if len(browsers) == 0 {
		return "", "", fmt.Errorf("headless browser (chrome/msedge) not found on system")
	}

	targetURL := trimmedTarget
	if !isHTTP && !strings.HasPrefix(targetURL, "file://") {
		if _, statErr := os.Stat(localFullPath); statErr != nil {
			return "", "", fmt.Errorf("target file not found: %s", localFullPath)
		}
		cleanPath := filepath.ToSlash(localFullPath)
		if !strings.HasPrefix(cleanPath, "/") {
			cleanPath = "/" + cleanPath
		}
		targetURL = "file://" + cleanPath
	}

	tmpFile, err := os.CreateTemp("", "unjess_vqa_*.png")
	if err != nil {
		return "", "", fmt.Errorf("failed to create temp file for screenshot: %w", err)
	}
	tmpPNG := tmpFile.Name()
	_ = tmpFile.Close()
	// Remove the pre-created 0-byte file so Chrome/Edge writes a brand-new PNG file cleanly
	_ = os.Remove(tmpPNG)
	defer os.Remove(tmpPNG)

	var lastOut string
	var lastErr error
	var usedBrowser string

	for _, browserPath := range browsers {
		for _, headlessFlag := range []string{"--headless=new", "--headless"} {
			_ = os.Remove(tmpPNG)
			tmpProfileDir, pErr := os.MkdirTemp("", "unjess_vqa_profile_*")
			if pErr != nil {
				continue
			}

			captureCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			args := []string{
				headlessFlag,
				"--user-data-dir=" + tmpProfileDir,
				"--no-first-run",
				"--no-default-browser-check",
				"--allow-file-access-from-files",
				"--disable-gpu",
				"--no-sandbox",
				"--disable-background-networking",
				"--disable-extensions",
				"--mute-audio",
				"--hide-scrollbars",
				fmt.Sprintf("--window-size=%d,%d", width, height),
				fmt.Sprintf("--screenshot=%s", tmpPNG),
				targetURL,
			}
			cmd := exec.CommandContext(captureCtx, browserPath, args...)
			setupProcessTreeKill(cmd)
			var outBuf bytes.Buffer
			cmd.Stdout = &outBuf
			cmd.Stderr = &outBuf
			runErr := cmd.Run()
			cancel()
			_ = os.RemoveAll(tmpProfileDir)

			lastOut = outBuf.String()
			lastErr = runErr

			if fi, statErr := os.Stat(tmpPNG); statErr == nil && fi.Size() > 0 {
				usedBrowser = browserPath
				break
			}
		}
		if usedBrowser != "" {
			break
		}
	}

	if usedBrowser == "" {
		if lastErr != nil {
			return "", "", fmt.Errorf("headless browser screenshot failed (%v): %s", lastErr, lastOut)
		}
		return "", "", fmt.Errorf("screenshot file is empty or unreadable (output: %s)", lastOut)
	}

	// Optimize captured screenshot PNG via LoadAndOptimizeImageForVision so large viewports (e.g. 1400x2400)
	// are automatically scaled to <= 1568px JPEG for reliable Vision LLM ingestion.
	dataURI, mimeType, imgW, imgH, origSz, optSz, optErr := LoadAndOptimizeImageForVision(tmpPNG, 1568)
	if optErr != nil {
		data, err := os.ReadFile(tmpPNG)
		if err != nil || len(data) == 0 {
			return "", "", fmt.Errorf("failed reading captured screenshot: %v", err)
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		dataURI = "data:image/png;base64," + b64
		metaReport = fmt.Sprintf("Visual QA Screenshot captured: %dx%d (%d KB) via %s for %s — attached to your Vision input for visual inspection.",
			width, height, len(data)/1024, filepath.Base(usedBrowser), targetURL)
		return dataURI, metaReport, nil
	}

	metaReport = fmt.Sprintf("Visual QA Screenshot captured and optimized (%s, %dx%d px, %d KB -> %d KB) via %s for %s — attached to your Vision input for visual inspection.",
		mimeType, imgW, imgH, origSz/1024, optSz/1024, filepath.Base(usedBrowser), targetURL)

	return dataURI, metaReport, nil
}

// -----------------------------------------------------------------------------
// #14 Background Watchdog / Ghost-Coder Sentinel
// -----------------------------------------------------------------------------

// WatchdogIssue represents a compiler or syntax issue identified in the workspace.
type WatchdogIssue struct {
	File     string `json:"file"`
	Severity string `json:"severity"` // "error", "warning"
	Message  string `json:"message"`
}

// WatchdogStatus represents the aggregate health check status of the workspace.
type WatchdogStatus struct {
	Workspace        string          `json:"workspace"`
	Healthy          bool            `json:"healthy"`
	Issues           []WatchdogIssue `json:"issues"`
	Summary          string          `json:"summary"`
	SuggestedPrompt  string          `json:"suggested_prompt"`
	CheckedAt        string          `json:"checked_at"`
	LastModifiedFile string          `json:"last_modified_file,omitempty"`
}

type touchedFile struct {
	path    string
	ext     string
	modTime time.Time
}

// CheckWorkspaceHealth analyzes recent source code changes and executes non-destructive syntax checks.
func CheckWorkspaceHealth(ctx context.Context, wsPath string) WatchdogStatus {
	status := WatchdogStatus{
		Workspace: wsPath,
		Healthy:   true,
		Issues:    []WatchdogIssue{},
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if wsPath == "" {
		status.Summary = "No workspace path specified"
		return status
	}

	// Scan workspace for recent source files
	var files []touchedFile
	ignoreDirs := map[string]bool{
		".git":         true,
		"node_modules": true,
		"vendor":       true,
		".unjess":      true,
		"bin":          true,
		"dist":         true,
		"build":        true,
		".idea":        true,
		".vscode":      true,
		"target":       true,
	}

	supportedExts := map[string]bool{
		".go": true,
		".py": true,
		".js": true,
		".ts": true,
		".rs": true,
	}

	_ = filepath.WalkDir(wsPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ignoreDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if supportedExts[ext] {
			if fi, err := d.Info(); err == nil {
				files = append(files, touchedFile{
					path:    path,
					ext:     ext,
					modTime: fi.ModTime(),
				})
			}
		}
		return nil
	})

	if len(files) == 0 {
		status.Summary = "No source files found in workspace."
		return status
	}

	// Sort most recently modified first
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	if rel, err := filepath.Rel(wsPath, files[0].path); err == nil {
		status.LastModifiedFile = rel
	} else {
		status.LastModifiedFile = files[0].path
	}

	// Run checks on up to top 3 recently touched files
	limit := 3
	if len(files) < limit {
		limit = len(files)
	}

	checkedLangs := make(map[string]bool)
	for i := 0; i < limit; i++ {
		file := files[i]
		relPath, _ := filepath.Rel(wsPath, file.path)
		if relPath == "" {
			relPath = file.path
		}

		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		switch file.ext {
		case ".go":
			if !checkedLangs["go"] {
				checkedLangs["go"] = true
				// Check directory of file or module
				dir := filepath.Dir(file.path)
				cmd := exec.CommandContext(checkCtx, "go", "vet", ".")
				cmd.Dir = dir
				setupProcessTreeKill(cmd)
				out, err := cmd.CombinedOutput()
				if err != nil {
					status.Issues = append(status.Issues, WatchdogIssue{
						File:     relPath,
						Severity: "error",
						Message:  strings.TrimSpace(string(out)),
					})
				}
			}
		case ".py":
			pyExec := "python"
			if runtime.GOOS != "windows" {
				if _, err := exec.LookPath("python3"); err == nil {
					pyExec = "python3"
				}
			}
			cmd := exec.CommandContext(checkCtx, pyExec, "-m", "py_compile", file.path)
			setupProcessTreeKill(cmd)
			out, err := cmd.CombinedOutput()
			if err != nil {
				status.Issues = append(status.Issues, WatchdogIssue{
					File:     relPath,
					Severity: "error",
					Message:  strings.TrimSpace(string(out)),
				})
			}
		case ".js":
			if _, err := exec.LookPath("node"); err == nil {
				cmd := exec.CommandContext(checkCtx, "node", "--check", file.path)
				setupProcessTreeKill(cmd)
				out, err := cmd.CombinedOutput()
				if err != nil {
					status.Issues = append(status.Issues, WatchdogIssue{
						File:     relPath,
						Severity: "error",
						Message:  strings.TrimSpace(string(out)),
					})
				}
			}
		case ".ts":
			if _, err := exec.LookPath("tsc"); err == nil {
				cmd := exec.CommandContext(checkCtx, "tsc", "--noEmit", file.path)
				setupProcessTreeKill(cmd)
				out, err := cmd.CombinedOutput()
				if err != nil {
					status.Issues = append(status.Issues, WatchdogIssue{
						File:     relPath,
						Severity: "error",
						Message:  strings.TrimSpace(string(out)),
					})
				}
			}
		case ".rs":
			if !checkedLangs["rs"] {
				if _, err := exec.LookPath("cargo"); err == nil {
					cargoToml := filepath.Join(wsPath, "Cargo.toml")
					if _, err := os.Stat(cargoToml); err == nil {
						checkedLangs["rs"] = true
						cmd := exec.CommandContext(checkCtx, "cargo", "check")
						cmd.Dir = wsPath
						setupProcessTreeKill(cmd)
						out, err := cmd.CombinedOutput()
						if err != nil {
							status.Issues = append(status.Issues, WatchdogIssue{
								File:     relPath,
								Severity: "error",
								Message:  strings.TrimSpace(string(out)),
							})
						}
					}
				}
			}
		}
	}

	if len(status.Issues) > 0 {
		status.Healthy = false
		status.Summary = fmt.Sprintf("Detected %d issue(s) during workspace health check", len(status.Issues))
		status.SuggestedPrompt = fmt.Sprintf("Fix the compiler/linter error detected by Watchdog in %s:\n%s",
			status.Issues[0].File, status.Issues[0].Message)
	} else {
		status.Healthy = true
		status.Summary = "All workspace compiler and syntax checks passed."
	}

	return status
}
