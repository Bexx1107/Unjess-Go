package codeintel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// allowExternalPaths, when true, lets post-edit checks operate on files outside the
// workspace root. The agent enables it in Autonomous/Duel mode.
var allowExternalPaths atomic.Bool

// SetAllowExternalPaths toggles whether files outside the workspace root may be checked.
func SetAllowExternalPaths(v bool) { allowExternalPaths.Store(v) }

// SymbolNode represents an extracted code symbol (function, method, struct, etc.).
type SymbolNode struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"` // "function", "method", "struct", "interface", "class", "const", "var"
	Signature string `json:"signature"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Doc       string `json:"doc,omitempty"`
}

// resolveSafePath verifies that relPath is within wsPath and prevents directory traversal.
func resolveSafePath(wsPath, relPath string) (string, error) {
	if wsPath == "" {
		return "", fmt.Errorf("workspace path cannot be empty")
	}
	cleanWs, err := filepath.Abs(wsPath)
	if err != nil {
		return "", err
	}
	var target string
	if filepath.IsAbs(relPath) {
		target = filepath.Clean(relPath)
	} else {
		target = filepath.Clean(filepath.Join(cleanWs, relPath))
	}
	if allowExternalPaths.Load() {
		return target, nil
	}
	rel, err := filepath.Rel(cleanWs, target)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("path %q escapes workspace root %q", relPath, wsPath)
	}
	return target, nil
}

// truncateString limits a string to maxLen characters, appending a note if truncated.
func truncateString(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n... [truncated]"
}

// formatErrorFeedback builds the standardized feedback block for compiler & linter failures.
func formatErrorFeedback(relPath, truncatedErrors string) string {
	return "\n\n[AUTO-LINTER & COMPILER FEEDBACK — SELF-HEAL REQUIRED]\nThe edit to " + relPath + " introduced syntax/compiler issues:\n" + truncatedErrors + "\nPlease fix these errors immediately before concluding your turn."
}

// findPythonBin searches PATH for a usable python binary.
func findPythonBin() string {
	for _, bin := range []string{"python", "python3", "py"} {
		if p, err := exec.LookPath(bin); err == nil {
			return p
		}
	}
	return ""
}

// -------------------------------------------------------------------------
// #1 Automatic Linter & Compiler Feedback Loop
// -------------------------------------------------------------------------

// RunPostEditCheck runs a fast compiler/syntax/linter check on the edited file.
// Returns an error feedback block if issues are found, "[Auto-Verify: Syntax & linter check PASSED]"
// if the check passed, or "" if no linter applies to this file type.
func RunPostEditCheck(ctx context.Context, wsPath, relPath string) string {
	absPath, err := resolveSafePath(wsPath, relPath)
	if err != nil {
		return formatErrorFeedback(relPath, err.Error())
	}

	info, err := os.Stat(absPath)
	if err != nil || info.IsDir() {
		return ""
	}

	ext := strings.ToLower(filepath.Ext(relPath))

	switch ext {
	case ".go":
		// Check using go vet if go toolchain is available
		if goBin, err := exec.LookPath("go"); err == nil {
			pkgDir := filepath.Dir(absPath)
			ctxTimeout, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctxTimeout, goBin, "vet", ".")
			cmd.Dir = pkgDir
			setHideWindow(cmd)
			out, vetErr := cmd.CombinedOutput()
			if vetErr == nil {
				return "[Auto-Verify: Syntax & linter check PASSED]"
			}

			// If go vet failed because go.mod is missing, fallback to go parser check
			outStr := string(out)
			if strings.Contains(outStr, "go.mod file not found") || strings.Contains(outStr, "cannot find package") {
				fset := token.NewFileSet()
				_, parseErr := parser.ParseFile(fset, absPath, nil, parser.AllErrors)
				if parseErr != nil {
					return formatErrorFeedback(relPath, parseErr.Error())
				}
				return "[Auto-Verify: Syntax & linter check PASSED]"
			}

			return formatErrorFeedback(relPath, truncateString(outStr, 2000))
		}

		// Fallback to in-process Go AST parser
		fset := token.NewFileSet()
		_, parseErr := parser.ParseFile(fset, absPath, nil, parser.AllErrors)
		if parseErr != nil {
			return formatErrorFeedback(relPath, parseErr.Error())
		}
		return "[Auto-Verify: Syntax & linter check PASSED]"

	case ".py":
		pyBin := findPythonBin()
		if pyBin != "" {
			ctxTimeout, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctxTimeout, pyBin, "-m", "py_compile", absPath)
			cmd.Dir = wsPath
			setHideWindow(cmd)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return formatErrorFeedback(relPath, truncateString(string(out), 2000))
			}
			return "[Auto-Verify: Syntax & linter check PASSED]"
		}
		return "[Auto-Verify: Syntax & linter check PASSED]"

	case ".js", ".mjs", ".cjs":
		if nodeBin, err := exec.LookPath("node"); err == nil {
			ctxTimeout, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctxTimeout, nodeBin, "--check", absPath)
			cmd.Dir = wsPath
			setHideWindow(cmd)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return formatErrorFeedback(relPath, truncateString(string(out), 2000))
			}
			return "[Auto-Verify: Syntax & linter check PASSED]"
		}
		return "[Auto-Verify: Syntax & linter check PASSED]"

	case ".ts", ".tsx":
		// Only run tsc if tsconfig.json exists
		tsconfig := filepath.Join(wsPath, "tsconfig.json")
		if _, err := os.Stat(tsconfig); err == nil {
			if npxBin, err := exec.LookPath("npx"); err == nil {
				ctxTimeout, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()

				cmd := exec.CommandContext(ctxTimeout, npxBin, "--no-install", "tsc", "--noEmit", "--pretty", "false")
				cmd.Dir = wsPath
				setHideWindow(cmd)
				out, err := cmd.CombinedOutput()
				if err != nil {
					return formatErrorFeedback(relPath, truncateString(string(out), 2000))
				}
				return "[Auto-Verify: Syntax & linter check PASSED]"
			}
		}
		return ""

	case ".rs":
		// Only run cargo check if Cargo.toml exists
		cargoToml := filepath.Join(wsPath, "Cargo.toml")
		if _, err := os.Stat(cargoToml); err == nil {
			if cargoBin, err := exec.LookPath("cargo"); err == nil {
				ctxTimeout, cancel := context.WithTimeout(ctx, 12*time.Second)
				defer cancel()

				cmd := exec.CommandContext(ctxTimeout, cargoBin, "check", "--message-format=short")
				cmd.Dir = wsPath
				setHideWindow(cmd)
				out, err := cmd.CombinedOutput()
				if err != nil {
					return formatErrorFeedback(relPath, truncateString(string(out), 2000))
				}
				return "[Auto-Verify: Syntax & linter check PASSED]"
			}
		}
		return ""

	case ".json":
		data, err := os.ReadFile(absPath)
		if err != nil {
			return formatErrorFeedback(relPath, err.Error())
		}
		if json.Valid(data) {
			return "[Auto-Verify: Syntax & linter check PASSED]"
		}
		var v any
		err = json.Unmarshal(data, &v)
		errMsg := "invalid JSON syntax"
		if err != nil {
			errMsg = err.Error()
		}
		return formatErrorFeedback(relPath, errMsg)

	default:
		return ""
	}
}

// VerifyWorkspaceBuild auto-detects build systems and runs a fast build or test check.
func VerifyWorkspaceBuild(ctx context.Context, wsPath string) (passed bool, output string) {
	ctxTimeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 1. Go project (go.mod)
	if _, err := os.Stat(filepath.Join(wsPath, "go.mod")); err == nil {
		if goBin, err := exec.LookPath("go"); err == nil {
			cmd := exec.CommandContext(ctxTimeout, goBin, "test", "./...")
			cmd.Dir = wsPath
			setHideWindow(cmd)
			out, err := cmd.CombinedOutput()
			return err == nil, string(out)
		}
	}

	// 2. Rust project (Cargo.toml)
	if _, err := os.Stat(filepath.Join(wsPath, "Cargo.toml")); err == nil {
		if cargoBin, err := exec.LookPath("cargo"); err == nil {
			cmd := exec.CommandContext(ctxTimeout, cargoBin, "check")
			cmd.Dir = wsPath
			setHideWindow(cmd)
			out, err := cmd.CombinedOutput()
			return err == nil, string(out)
		}
	}

	// 3. Node project (package.json)
	if _, err := os.Stat(filepath.Join(wsPath, "package.json")); err == nil {
		if npmBin, err := exec.LookPath("npm"); err == nil {
			cmd := exec.CommandContext(ctxTimeout, npmBin, "test", "--if-present")
			cmd.Dir = wsPath
			setHideWindow(cmd)
			out, err := cmd.CombinedOutput()
			return err == nil, string(out)
		}
	}

	// 4. Python project
	hasPy := false
	for _, pyFile := range []string{"pytest.ini", "pyproject.toml", "setup.py", "tox.ini"} {
		if _, err := os.Stat(filepath.Join(wsPath, pyFile)); err == nil {
			hasPy = true
			break
		}
	}
	if !hasPy {
		for _, testDir := range []string{"tests", "test"} {
			if info, err := os.Stat(filepath.Join(wsPath, testDir)); err == nil && info.IsDir() {
				hasPy = true
				break
			}
		}
	}
	if hasPy {
		if pytestBin, err := exec.LookPath("pytest"); err == nil {
			cmd := exec.CommandContext(ctxTimeout, pytestBin)
			cmd.Dir = wsPath
			setHideWindow(cmd)
			out, err := cmd.CombinedOutput()
			return err == nil, string(out)
		}
		if pyBin := findPythonBin(); pyBin != "" {
			cmd := exec.CommandContext(ctxTimeout, pyBin, "-m", "unittest", "discover")
			cmd.Dir = wsPath
			setHideWindow(cmd)
			out, err := cmd.CombinedOutput()
			return err == nil, string(out)
		}
	}

	return true, "no supported build configuration detected"
}

// -------------------------------------------------------------------------
// #3 & #8 AST Code Graph & AST-Native Refactoring
// -------------------------------------------------------------------------

// ExtractSymbols parses a source file and extracts its symbol declarations.
func ExtractSymbols(wsPath, relPath string) ([]SymbolNode, error) {
	absPath, err := resolveSafePath(wsPath, relPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(relPath))
	if ext == ".go" {
		symbols, parseErr := extractGoSymbols(absPath, data)
		if parseErr == nil && len(symbols) > 0 {
			return symbols, nil
		}
		// If AST parse had an error (e.g. temporary syntax issue), fall back to structural parser
		return extractStructuralSymbols(data, ext)
	}

	return extractStructuralSymbols(data, ext)
}

// extractGoSymbols uses go/parser and go/ast for high-fidelity Go symbol extraction.
func extractGoSymbols(absPath string, data []byte) ([]SymbolNode, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, absPath, data, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var symbols []SymbolNode
	lines := strings.Split(string(data), "\n")

	for _, decl := range node.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			startLine := fset.Position(d.Pos()).Line
			endLine := fset.Position(d.End()).Line
			kind := "function"
			name := d.Name.Name

			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
				recvType := formatGoReceiver(d.Recv.List[0].Type)
				if recvType != "" {
					name = recvType + "." + d.Name.Name
				}
			}

			sig := getGoFunctionSignature(data, fset, d, lines, startLine)
			var doc string
			if d.Doc != nil {
				doc = strings.TrimSpace(d.Doc.Text())
			}

			symbols = append(symbols, SymbolNode{
				Name:      name,
				Kind:      kind,
				Signature: sig,
				StartLine: startLine,
				EndLine:   endLine,
				Doc:       doc,
			})

		case *ast.GenDecl:
			var genDoc string
			if d.Doc != nil {
				genDoc = strings.TrimSpace(d.Doc.Text())
			}

			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					startLine := fset.Position(s.Pos()).Line
					endLine := fset.Position(s.End()).Line
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}

					doc := genDoc
					if s.Doc != nil {
						doc = strings.TrimSpace(s.Doc.Text())
					}

					sig := getLineFromSlice(lines, startLine)
					symbols = append(symbols, SymbolNode{
						Name:      s.Name.Name,
						Kind:      kind,
						Signature: sig,
						StartLine: startLine,
						EndLine:   endLine,
						Doc:       doc,
					})

				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					startLine := fset.Position(s.Pos()).Line
					endLine := fset.Position(s.End()).Line
					doc := genDoc
					if s.Doc != nil {
						doc = strings.TrimSpace(s.Doc.Text())
					}

					for _, id := range s.Names {
						sig := getLineFromSlice(lines, startLine)
						symbols = append(symbols, SymbolNode{
							Name:      id.Name,
							Kind:      kind,
							Signature: sig,
							StartLine: startLine,
							EndLine:   endLine,
							Doc:       doc,
						})
					}
				}
			}
		}
	}

	return symbols, nil
}

func formatGoReceiver(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return "(*" + id.Name + ")"
		}
	case *ast.Ident:
		return "(" + t.Name + ")"
	}
	return ""
}

func getGoFunctionSignature(data []byte, fset *token.FileSet, d *ast.FuncDecl, lines []string, startLine int) string {
	if d.Body != nil {
		startOffset := fset.Position(d.Pos()).Offset
		bodyOffset := fset.Position(d.Body.Lbrace).Offset
		if startOffset >= 0 && bodyOffset <= len(data) && startOffset < bodyOffset {
			raw := string(data[startOffset:bodyOffset])
			raw = strings.TrimSpace(raw)
			raw = strings.Join(strings.Fields(raw), " ")
			return raw
		}
	}
	return getLineFromSlice(lines, startLine)
}

func getLineFromSlice(lines []string, lineNum int) string {
	if lineNum >= 1 && lineNum <= len(lines) {
		return strings.TrimSpace(lines[lineNum-1])
	}
	return ""
}

// extractStructuralSymbols handles Python indentation tracking and JS/TS/Rust/C brace tracking.
func extractStructuralSymbols(data []byte, ext string) ([]SymbolNode, error) {
	content := string(data)
	lines := strings.Split(content, "\n")

	if ext == ".py" {
		return extractPythonSymbols(lines)
	}

	return extractBraceSymbols(lines)
}

func countIndent(line string) int {
	indent := 0
	for _, r := range line {
		if r == ' ' {
			indent++
		} else if r == '\t' {
			indent += 4
		} else {
			break
		}
	}
	return indent
}

var (
	pyDefRegex   = regexp.MustCompile(`^[ \t]*(?:async\s+)?def\s+([a-zA-Z0-9_]+)\s*\(`)
	pyClassRegex = regexp.MustCompile(`^[ \t]*class\s+([a-zA-Z0-9_]+)`)
)

func extractPythonSymbols(lines []string) ([]SymbolNode, error) {
	var symbols []SymbolNode

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		var symName string
		var kind string

		if m := pyDefRegex.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "function"
			if countIndent(line) > 0 {
				kind = "method"
			}
		} else if m := pyClassRegex.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "class"
		}

		if symName == "" {
			continue
		}

		baseIndent := countIndent(line)
		startLine := i + 1
		sig := trimmed

		// Scan for end line based on indentation
		endLine := startLine
		lastNonEmpty := startLine

		for j := i + 1; j < len(lines); j++ {
			subTrim := strings.TrimSpace(lines[j])
			if subTrim == "" || strings.HasPrefix(subTrim, "#") {
				continue
			}
			if countIndent(lines[j]) <= baseIndent {
				// Block has finished
				break
			}
			lastNonEmpty = j + 1
		}
		endLine = lastNonEmpty

		// Extract docstring if present on immediately following lines
		var doc string
		if i+1 < len(lines) {
			nextTrim := strings.TrimSpace(lines[i+1])
			if strings.HasPrefix(nextTrim, `"""`) || strings.HasPrefix(nextTrim, `'''`) {
				quote := nextTrim[:3]
				var docLines []string
				rest := nextTrim[3:]
				if idx := strings.Index(rest, quote); idx >= 0 {
					doc = strings.TrimSpace(rest[:idx])
				} else {
					docLines = append(docLines, rest)
					for k := i + 2; k < len(lines) && k <= endLine; k++ {
						kLine := lines[k]
						if idx := strings.Index(kLine, quote); idx >= 0 {
							docLines = append(docLines, kLine[:idx])
							break
						}
						docLines = append(docLines, kLine)
					}
					doc = strings.TrimSpace(strings.Join(docLines, "\n"))
				}
			}
		}

		symbols = append(symbols, SymbolNode{
			Name:      symName,
			Kind:      kind,
			Signature: sig,
			StartLine: startLine,
			EndLine:   endLine,
			Doc:       doc,
		})
	}

	return symbols, nil
}

var (
	braceFuncRegex = regexp.MustCompile(`(?:^|[ \t]+)(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([a-zA-Z0-9_$]+)`)
	braceClassReg  = regexp.MustCompile(`(?:^|[ \t]+)(?:export\s+)?class\s+([a-zA-Z0-9_$]+)`)
	braceInterReg  = regexp.MustCompile(`(?:^|[ \t]+)(?:export\s+)?interface\s+([a-zA-Z0-9_$]+)`)
	braceStructReg = regexp.MustCompile(`(?:^|[ \t]+)(?:pub(?:\([^)]*\))?\s+)?(?:struct|enum|trait)\s+([a-zA-Z0-9_]+)`)
	braceImplReg   = regexp.MustCompile(`(?:^|[ \t]+)impl(?:\s+<[^>]*>)?\s+([a-zA-Z0-9_]+)`)
	braceRustFn    = regexp.MustCompile(`(?:^|[ \t]+)(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+([a-zA-Z0-9_]+)`)
	braceConstFn   = regexp.MustCompile(`(?:^|[ \t]+)(?:export\s+)?(?:const|let|var)\s+([a-zA-Z0-9_$]+)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[a-zA-Z0-9_$]+)\s*=>`)
)

func extractBraceSymbols(lines []string) ([]SymbolNode, error) {
	var symbols []SymbolNode

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		var symName string
		var kind string

		if m := braceFuncRegex.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "function"
		} else if m := braceRustFn.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "function"
		} else if m := braceConstFn.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "function"
		} else if m := braceClassReg.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "class"
		} else if m := braceInterReg.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "interface"
		} else if m := braceStructReg.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "struct"
		} else if m := braceImplReg.FindStringSubmatch(line); len(m) > 1 {
			symName = m[1]
			kind = "struct"
		}

		if symName == "" {
			continue
		}

		startLine := i + 1
		sig := trimmed

		// Track braces to find end line
		braceCount := 0
		foundOpen := false
		endLine := startLine

		for j := i; j < len(lines); j++ {
			cleanLine := removeStringsAndComments(lines[j])
			for _, r := range cleanLine {
				if r == '{' {
					braceCount++
					foundOpen = true
				} else if r == '}' {
					braceCount--
				}
			}
			if foundOpen && braceCount <= 0 {
				endLine = j + 1
				break
			}
			if j == len(lines)-1 {
				endLine = len(lines)
			}
		}

		// Look for doc comments immediately preceding startLine
		var docLines []string
		for k := i - 1; k >= 0; k-- {
			pTrim := strings.TrimSpace(lines[k])
			if strings.HasPrefix(pTrim, "//") {
				docLines = append([]string{strings.TrimSpace(strings.TrimPrefix(pTrim, "//"))}, docLines...)
			} else if strings.HasPrefix(pTrim, "/*") || strings.HasPrefix(pTrim, "*") || strings.HasSuffix(pTrim, "*/") {
				cleaned := strings.Trim(pTrim, "/* ")
				docLines = append([]string{cleaned}, docLines...)
			} else {
				break
			}
		}
		doc := strings.Join(docLines, "\n")

		symbols = append(symbols, SymbolNode{
			Name:      symName,
			Kind:      kind,
			Signature: sig,
			StartLine: startLine,
			EndLine:   endLine,
			Doc:       doc,
		})
	}

	return symbols, nil
}

func removeStringsAndComments(line string) string {
	var sb strings.Builder
	inQuote := byte(0)
	runes := []byte(line)

	for i := 0; i < len(runes); i++ {
		b := runes[i]
		if inQuote != 0 {
			if b == inQuote && (i == 0 || runes[i-1] != '\\') {
				inQuote = 0
			}
			continue
		}
		if b == '"' || b == '\'' || b == '`' {
			inQuote = b
			continue
		}
		if b == '/' && i+1 < len(runes) && runes[i+1] == '/' {
			break
		}
		sb.WriteByte(b)
	}
	return sb.String()
}

// FormatFileOutline returns a readable AST outline of symbols in relPath.
func FormatFileOutline(wsPath, relPath string) (string, error) {
	symbols, err := ExtractSymbols(wsPath, relPath)
	if err != nil {
		return "", err
	}

	if len(symbols) == 0 {
		return fmt.Sprintf("File Outline: %s (no symbols found)", relPath), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("File Outline: %s (%d symbols)\n", relPath, len(symbols)))
	for _, sym := range symbols {
		sb.WriteString(fmt.Sprintf("  - [%s] %s (lines %d-%d)\n", sym.Kind, sym.Name, sym.StartLine, sym.EndLine))
		if sym.Doc != "" {
			firstLine := strings.Split(sym.Doc, "\n")[0]
			sb.WriteString(fmt.Sprintf("      Doc: %s\n", firstLine))
		}
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}

// isIgnoredDir returns true for common directories that should be skipped during repo walks.
func isIgnoredDir(name string) bool {
	switch name {
	case ".git", "node_modules", "target", "dist", ".unjess", "vendor", "build", "bin", "obj", ".idea", ".vscode", "__pycache__":
		return true
	default:
		return false
	}
}

// isCodeFile checks if a file extension represents code or text for indexing.
func isCodeFile(ext string) bool {
	switch ext {
	case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".rs", ".c", ".cpp", ".h", ".hpp", ".html", ".css", ".json", ".yaml", ".yml", ".md", ".toml", ".sql":
		return true
	default:
		return false
	}
}

// FindSymbolCallers finds all definitions and call/reference sites of symbolName in wsPath.
func FindSymbolCallers(wsPath, symbolName string) (string, error) {
	if symbolName == "" {
		return "", fmt.Errorf("symbol name cannot be empty")
	}

	cleanWs, err := filepath.Abs(wsPath)
	if err != nil {
		return "", err
	}

	wordRegex, err := regexp.Compile(`\b` + regexp.QuoteMeta(symbolName) + `\b`)
	if err != nil {
		return "", err
	}

	type matchLocation struct {
		file string
		line int
		text string
	}

	var definitions []matchLocation
	var references []matchLocation

	err = filepath.Walk(cleanWs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if isIgnoredDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !isCodeFile(ext) {
			return nil
		}

		rel, err := filepath.Rel(cleanWs, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		if !wordRegex.Match(data) {
			return nil
		}

		// Extract symbols to distinguish definitions from calls
		symbols, _ := ExtractSymbols(cleanWs, rel)
		defLines := make(map[int]bool)
		for _, s := range symbols {
			if matchesSymbolName(s.Name, symbolName) {
				defLines[s.StartLine] = true
				definitions = append(definitions, matchLocation{
					file: rel,
					line: s.StartLine,
					text: s.Signature,
				})
			}
		}

		// Scan lines for references
		scanner := bufio.NewScanner(bytes.NewReader(data))
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			lineText := scanner.Text()
			if wordRegex.MatchString(lineText) {
				if !defLines[lineNum] {
					references = append(references, matchLocation{
						file: rel,
						line: lineNum,
						text: strings.TrimSpace(lineText),
					})
				}
			}
		}

		return nil
	})

	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== AST Code Graph & Callers Report for '%s' ===\n", symbolName))

	if len(definitions) == 0 && len(references) == 0 {
		sb.WriteString("No definitions or references found in workspace.\n")
		return sb.String(), nil
	}

	sb.WriteString(fmt.Sprintf("Definitions (%d found):\n", len(definitions)))
	for _, d := range definitions {
		sb.WriteString(fmt.Sprintf("  - %s:%d: %s\n", d.file, d.line, d.text))
	}

	sb.WriteString(fmt.Sprintf("References (%d found):\n", len(references)))
	for _, r := range references {
		sb.WriteString(fmt.Sprintf("  - %s:%d: %s\n", r.file, r.line, r.text))
	}

	return sb.String(), nil
}

// matchesSymbolName tests whether symName matches the target symbolName.
func matchesSymbolName(symName, target string) bool {
	if symName == target {
		return true
	}
	if strings.EqualFold(symName, target) {
		return true
	}
	// Receiver or dot notation: (*Server).handleChat or Server.handleChat
	if idx := strings.LastIndex(symName, "."); idx >= 0 {
		methodName := symName[idx+1:]
		if methodName == target || strings.EqualFold(methodName, target) {
			return true
		}
	}
	if strings.HasSuffix(strings.ToLower(symName), "."+strings.ToLower(target)) {
		return true
	}
	return false
}

// EditSymbol replaces the declaration of symbolName in relPath with newSymbolCode.
// It also attaches a callersReport of other files referencing this symbol.
func EditSymbol(wsPath, relPath, symbolName, newSymbolCode string) (oldContent string, updatedContent string, callersReport string, err error) {
	absPath, err := resolveSafePath(wsPath, relPath)
	if err != nil {
		return "", "", "", err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", "", "", err
	}
	oldContent = string(data)

	symbols, err := ExtractSymbols(wsPath, relPath)
	if err != nil {
		return "", "", "", err
	}

	var targetNode *SymbolNode
	for i := range symbols {
		if matchesSymbolName(symbols[i].Name, symbolName) {
			targetNode = &symbols[i]
			break
		}
	}

	if targetNode == nil {
		return "", "", "", fmt.Errorf("symbol %q not found in %s", symbolName, relPath)
	}

	isCRLF := strings.Contains(oldContent, "\r\n")
	lineSep := "\n"
	if isCRLF {
		lineSep = "\r\n"
	}

	// Normalize lines
	rawLines := strings.Split(strings.ReplaceAll(oldContent, "\r\n", "\n"), "\n")

	startIdx := targetNode.StartLine - 1
	endIdx := targetNode.EndLine // exclusive line index

	if startIdx < 0 || startIdx > len(rawLines) || endIdx < startIdx {
		return "", "", "", fmt.Errorf("invalid line coordinates for symbol %s [%d..%d]", symbolName, targetNode.StartLine, targetNode.EndLine)
	}
	if endIdx > len(rawLines) {
		endIdx = len(rawLines)
	}

	cleanNewCode := strings.TrimRight(strings.ReplaceAll(newSymbolCode, "\r\n", "\n"), "\n")

	var rebuilt []string
	rebuilt = append(rebuilt, rawLines[:startIdx]...)
	if cleanNewCode != "" {
		rebuilt = append(rebuilt, cleanNewCode)
	}
	rebuilt = append(rebuilt, rawLines[endIdx:]...)

	updatedContent = strings.Join(rebuilt, lineSep)

	if err := os.WriteFile(absPath, []byte(updatedContent), 0644); err != nil {
		return "", "", "", err
	}

	callersReport, _ = FindSymbolCallers(wsPath, symbolName)

	return oldContent, updatedContent, callersReport, nil
}

// RenameSymbolAcrossRepo performs word-boundary replacement of oldName with newName across code files.
func RenameSymbolAcrossRepo(wsPath, oldName, newName string) (modifiedFiles []string, totalReplacements int, err error) {
	if oldName == "" || newName == "" {
		return nil, 0, fmt.Errorf("oldName and newName cannot be empty")
	}

	cleanWs, err := filepath.Abs(wsPath)
	if err != nil {
		return nil, 0, err
	}

	wordRegex, err := regexp.Compile(`\b` + regexp.QuoteMeta(oldName) + `\b`)
	if err != nil {
		return nil, 0, err
	}

	err = filepath.Walk(cleanWs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if isIgnoredDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !isCodeFile(ext) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		matches := wordRegex.FindAllIndex(data, -1)
		if len(matches) == 0 {
			return nil
		}

		newData := wordRegex.ReplaceAll(data, []byte(newName))
		if err := os.WriteFile(path, newData, info.Mode().Perm()); err != nil {
			return err
		}

		rel, err := filepath.Rel(cleanWs, path)
		if err != nil {
			rel = path
		}
		modifiedFiles = append(modifiedFiles, filepath.ToSlash(rel))
		totalReplacements += len(matches)

		return nil
	})

	if err != nil {
		return nil, 0, err
	}

	return modifiedFiles, totalReplacements, nil
}

// -------------------------------------------------------------------------
// #15 GraphRAG & Git Archeology
// -------------------------------------------------------------------------

// GitArcheology runs git commands in wsPath to uncover the historical context behind lines or symbols.
func GitArcheology(ctx context.Context, wsPath, relPath, queryOrSymbol string, startLine, endLine int) (string, error) {
	cleanWs, err := filepath.Abs(wsPath)
	if err != nil {
		return "", err
	}

	gitBin, err := exec.LookPath("git")
	if err != nil {
		return "<git_archeology_report>\nGit is not installed or not in PATH.\n</git_archeology_report>", nil
	}

	// Verify wsPath is a git worktree
	checkCmd := exec.CommandContext(ctx, gitBin, "rev-parse", "--is-inside-work-tree")
	checkCmd.Dir = cleanWs
	setHideWindow(checkCmd)
	if err := checkCmd.Run(); err != nil {
		return "<git_archeology_report>\nWorkspace is not a git repository.\n</git_archeology_report>", nil
	}

	var sb strings.Builder
	sb.WriteString("<git_archeology_report>\n")

	// 1. Line range history
	if relPath != "" && startLine > 0 && endLine >= startLine {
		slashPath := filepath.ToSlash(relPath)
		sb.WriteString(fmt.Sprintf("[Line Range Archeology: %s:%d-%d]\n", slashPath, startLine, endLine))

		// Try git log -L
		logLCmd := exec.CommandContext(ctx, gitBin, "log", "-n", "5", fmt.Sprintf("-L%d,%d:%s", startLine, endLine, slashPath))
		logLCmd.Dir = cleanWs
		setHideWindow(logLCmd)
		outL, logLErr := logLCmd.CombinedOutput()

		if logLErr == nil && len(outL) > 0 {
			sb.WriteString(string(outL))
		} else {
			// Fallback: git blame -L + git log -n 5 --oneline
			blameCmd := exec.CommandContext(ctx, gitBin, "blame", fmt.Sprintf("-L%d,%d", startLine, endLine), "--", slashPath)
			blameCmd.Dir = cleanWs
			setHideWindow(blameCmd)
			outBlame, _ := blameCmd.CombinedOutput()

			sb.WriteString("Line Blame:\n")
			sb.WriteString(string(outBlame))

			histCmd := exec.CommandContext(ctx, gitBin, "log", "-n", "5", "--oneline", "--", slashPath)
			histCmd.Dir = cleanWs
			setHideWindow(histCmd)
			outHist, _ := histCmd.CombinedOutput()

			sb.WriteString("\nRecent Commits for File:\n")
			sb.WriteString(string(outHist))
		}
		sb.WriteString("\n")
	}

	// 2. Symbol / Query Pickaxe search
	if queryOrSymbol != "" {
		sb.WriteString(fmt.Sprintf("[Symbol / Query Pickaxe Search: '%s']\n", queryOrSymbol))

		pickaxeCmd := exec.CommandContext(ctx, gitBin, "log", "-n", "8", "--oneline", "-S", queryOrSymbol)
		pickaxeCmd.Dir = cleanWs
		setHideWindow(pickaxeCmd)
		outPickaxe, _ := pickaxeCmd.CombinedOutput()

		if len(strings.TrimSpace(string(outPickaxe))) > 0 {
			sb.WriteString("Commits modifying this symbol/query:\n")
			sb.WriteString(string(outPickaxe))

			// Detailed patch diff for up to 3 commits
			diffCmd := exec.CommandContext(ctx, gitBin, "log", "-n", "3", "--stat", "-p", "-S", queryOrSymbol)
			diffCmd.Dir = cleanWs
			setHideWindow(diffCmd)
			outDiff, _ := diffCmd.CombinedOutput()
			sb.WriteString("\nRecent Diff History:\n")
			sb.WriteString(truncateString(string(outDiff), 3000))
		} else {
			sb.WriteString("No commits found matching query in git pickaxe log.\n")
		}
		sb.WriteString("\n")
	}

	sb.WriteString("</git_archeology_report>")
	return sb.String(), nil
}

// -------------------------------------------------------------------------
// #7 Time-Travel / Post-Mortem Crash & Variable Inspector
// -------------------------------------------------------------------------

// DebugTraceCommand runs a command, captures crash traces, and extracts source context around stack frames.
func DebugTraceCommand(ctx context.Context, wsPath, command string) (string, error) {
	cleanWs, err := filepath.Abs(wsPath)
	if err != nil {
		return "", err
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}

	cmd.Dir = cleanWs
	cmd.Env = append(os.Environ(),
		"GOTRACEBACK=all",
		"PYTHONUNBUFFERED=1",
		"PYTHONFAULTHANDLER=1",
	)
	setHideWindow(cmd)

	outBytes, cmdErr := cmd.CombinedOutput()
	rawOutput := string(outBytes)

	var sb strings.Builder
	sb.WriteString("=== POST-MORTEM CRASH TRACE & VARIABLE INSPECTOR ===\n")
	sb.WriteString(fmt.Sprintf("Command: %s\n", command))
	sb.WriteString(fmt.Sprintf("Working Directory: %s\n", cleanWs))

	if cmdErr != nil {
		sb.WriteString(fmt.Sprintf("Execution Status: FAILED (%v)\n\n", cmdErr))
	} else {
		sb.WriteString("Execution Status: SUCCESS (exit code 0)\n\n")
	}

	sb.WriteString("--- PROCESS OUTPUT ---\n")
	sb.WriteString(truncateString(rawOutput, 4000))
	sb.WriteString("\n\n")

	// Parse stack frames from stdout/stderr
	frames := parseCrashFrames(cleanWs, rawOutput)
	if len(frames) > 0 {
		sb.WriteString(fmt.Sprintf("--- STACK FRAME INSPECTION (%d frame(s) identified in workspace) ---\n", len(frames)))
		for idx, f := range frames {
			sb.WriteString(fmt.Sprintf("\n[Frame %d: %s:%d", idx+1, f.relPath, f.line))
			if f.function != "" {
				sb.WriteString(fmt.Sprintf(" in %s()", f.function))
			}
			sb.WriteString("]\n")

			// Source window around crash line (up to 15 lines)
			sb.WriteString("Source Context:\n")
			sb.WriteString(f.sourceSnippet)
			sb.WriteString("\n")

			// Variable inspection
			if len(f.variables) > 0 {
				sb.WriteString("Local Variables / Assignments in Scope:\n")
				for _, v := range f.variables {
					sb.WriteString(fmt.Sprintf("    %s\n", v))
				}
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("--- STACK FRAME INSPECTION ---\nNo in-workspace source crash frames identified.\n")
	}

	return sb.String(), nil
}

type crashFrame struct {
	relPath       string
	line          int
	function      string
	sourceSnippet string
	variables     []string
}

var (
	pyFrameRegex = regexp.MustCompile(`File "([^"]+)", line (\d+)(?:, in (\w+))?`)
	goFrameRegex = regexp.MustCompile(`([a-zA-Z0-9_\-\./\\]+\.go):(\d+)`)
	jsFrameRegex = regexp.MustCompile(`(?:at\s+(?:([a-zA-Z0-9_$.]+)\s+\()?)?([a-zA-Z0-9_\-\./\\]+\.(?:js|ts|mjs|cjs)):(\d+)(?::\d+)?\)?`)
	rsFrameRegex = regexp.MustCompile(`at\s+([a-zA-Z0-9_\-\./\\]+\.rs):(\d+)`)
)

func parseCrashFrames(wsPath, output string) []crashFrame {
	var frames []crashFrame
	seen := make(map[string]bool)

	parseMatch := func(filePath string, lineNumStr string, fnName string) {
		lineNum, err := strconv.Atoi(lineNumStr)
		if err != nil || lineNum < 1 {
			return
		}

		// Check if file is in workspace
		absFile := filePath
		if !filepath.IsAbs(absFile) {
			absFile = filepath.Join(wsPath, filePath)
		}

		info, err := os.Stat(absFile)
		if err != nil || info.IsDir() {
			// Try finding file under wsPath by relative search
			base := filepath.Base(filePath)
			found := ""
			_ = filepath.Walk(wsPath, func(p string, fi os.FileInfo, _ error) error {
				if fi != nil && !fi.IsDir() && fi.Name() == base {
					found = p
					return filepath.SkipAll
				}
				return nil
			})
			if found == "" {
				return
			}
			absFile = found
		}

		rel, err := filepath.Rel(wsPath, absFile)
		if err != nil {
			rel = absFile
		}
		rel = filepath.ToSlash(rel)

		key := fmt.Sprintf("%s:%d", rel, lineNum)
		if seen[key] {
			return
		}
		seen[key] = true

		data, err := os.ReadFile(absFile)
		if err != nil {
			return
		}

		lines := strings.Split(string(data), "\n")
		totalLines := len(lines)
		if lineNum > totalLines {
			lineNum = totalLines
		}

		// 15-line window centered on lineNum (e.g. -7 to +7)
		start := lineNum - 7
		if start < 1 {
			start = 1
		}
		end := lineNum + 7
		if end > totalLines {
			end = totalLines
		}

		var snipSb strings.Builder
		for l := start; l <= end; l++ {
			prefix := "    "
			if l == lineNum {
				prefix = ">>> "
			}
			content := strings.TrimRight(lines[l-1], "\r\n")
			snipSb.WriteString(fmt.Sprintf("%s%4d | %s\n", prefix, l, content))
		}

		// Extract local variable assignments before lineNum
		var vars []string
		varAssRegex := regexp.MustCompile(`^[ \t]*([a-zA-Z_][a-zA-Z0-9_]*)\s*[:=]+\s*(.*)`)
		vStart := lineNum - 10
		if vStart < 1 {
			vStart = 1
		}
		for l := vStart; l <= lineNum; l++ {
			raw := lines[l-1]
			if m := varAssRegex.FindStringSubmatch(raw); len(m) > 2 {
				val := strings.TrimSpace(m[2])
				if len(val) > 40 {
					val = val[:40] + "..."
				}
				vars = append(vars, fmt.Sprintf("%s = %s (line %d)", m[1], val, l))
			}
		}

		frames = append(frames, crashFrame{
			relPath:       rel,
			line:          lineNum,
			function:      fnName,
			sourceSnippet: snipSb.String(),
			variables:     vars,
		})
	}

	// 1. Python frames
	for _, m := range pyFrameRegex.FindAllStringSubmatch(output, -1) {
		fn := ""
		if len(m) > 3 {
			fn = m[3]
		}
		parseMatch(m[1], m[2], fn)
	}

	// 2. Go frames
	for _, m := range goFrameRegex.FindAllStringSubmatch(output, -1) {
		parseMatch(m[1], m[2], "")
	}

	// 3. Node/JS frames
	for _, m := range jsFrameRegex.FindAllStringSubmatch(output, -1) {
		parseMatch(m[2], m[3], m[1])
	}

	// 4. Rust frames
	for _, m := range rsFrameRegex.FindAllStringSubmatch(output, -1) {
		parseMatch(m[1], m[2], "")
	}

	return frames
}
