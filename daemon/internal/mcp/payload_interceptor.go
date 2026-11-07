package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// dataURIRegex matches data:[<mediatype>][;base64],<data>
	dataURIRegex = regexp.MustCompile(`data:([a-zA-Z0-9\+\-\_\.\/]+);base64,([A-Za-z0-9+/=\r\n]{64,})`)
)

// InterceptAndSavePayloads inspects an MCP tool call output for large binary / Base64 file payloads
// (e.g. PDF attachments, images, archives) returned by email, fetch, or file MCP tools.
// It decodes the binary payloads, saves them to <workspaceDir>/downloads/, and replaces the
// payload in the output with a clean disk reference so LLM context windows do not blow up or get truncated.
func InterceptAndSavePayloads(workspaceDir string, output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return output
	}

	downloadsDir := resolveDownloadsDir(workspaceDir)

	// 1. Try parsing output as JSON structure (typical for email/file MCP servers)
	var jsonData any
	if err := json.Unmarshal([]byte(output), &jsonData); err == nil {
		var savedFiles []string
		modifiedData, changed := walkAndInterceptJSON(jsonData, workspaceDir, downloadsDir, &savedFiles)
		if changed && len(savedFiles) > 0 {
			var resBytes []byte
			if strings.Contains(output, "\n") {
				resBytes, _ = json.MarshalIndent(modifiedData, "", "  ")
			} else {
				resBytes, _ = json.Marshal(modifiedData)
			}
			if len(resBytes) > 0 {
				return string(resBytes)
			}
		}
	}

	// 2. Check for embedded data URIs (e.g. data:application/pdf;base64,...)
	if strings.Contains(output, "data:") && strings.Contains(output, ";base64,") {
		output = dataURIRegex.ReplaceAllStringFunc(output, func(match string) string {
			sub := dataURIRegex.FindStringSubmatch(match)
			if len(sub) < 3 {
				return match
			}
			mime := strings.TrimSpace(sub[1])
			rawB64 := sub[2]

			data, ok := tryDecodeBase64(rawB64)
			if !ok || len(data) == 0 {
				return match
			}

			ext, detectedMime := detectBinaryExtension(data, mime)
			if ext == "" && len(data) < 1024 {
				return match
			}
			if detectedMime == "" {
				detectedMime = mime
			}
			if ext == "" {
				ext = ".bin"
			}

			name := fmt.Sprintf("attachment_%d%s", time.Now().UnixNano()/1e6, ext)
			relPath, err := saveBinaryFile(downloadsDir, workspaceDir, name, data)
			if err != nil {
				return match
			}

			return fmt.Sprintf("[Saved attachment to %s (size: %s, type: %s)]", relPath, formatBytes(len(data)), detectedMime)
		})
	}

	// 3. Check if the ENTIRE output is a raw Base64 binary block
	if len(trimmed) >= 128 && isBase64String(trimmed) {
		if data, ok := tryDecodeBase64(trimmed); ok && len(data) >= 32 {
			ext, detectedMime := detectBinaryExtension(data, "")
			if ext != "" {
				name := fmt.Sprintf("attachment_%d%s", time.Now().UnixNano()/1e6, ext)
				relPath, err := saveBinaryFile(downloadsDir, workspaceDir, name, data)
				if err == nil {
					return fmt.Sprintf("[MCP tool returned binary attachment. Successfully saved to %s (size: %s, type: %s)]", relPath, formatBytes(len(data)), detectedMime)
				}
			}
		}
	}

	return output
}

// walkAndInterceptJSON recursively traverses JSON objects/arrays looking for Base64 attachments.
func walkAndInterceptJSON(v any, wsDir, downloadsDir string, savedFiles *[]string) (any, bool) {
	changed := false

	switch val := v.(type) {
	case map[string]any:
		// Check for declared filename & mimeType in current object
		var declaredFilename string
		for _, k := range []string{"filename", "fileName", "name", "file_name", "title"} {
			if s, ok := val[k].(string); ok && strings.TrimSpace(s) != "" {
				declaredFilename = strings.TrimSpace(s)
				break
			}
		}
		var declaredMime string
		for _, k := range []string{"mimeType", "mime_type", "contentType", "content_type", "type"} {
			if s, ok := val[k].(string); ok && strings.TrimSpace(s) != "" {
				declaredMime = strings.TrimSpace(s)
				break
			}
		}

		// Inspect all fields for Base64 binary contents
		for k, item := range val {
			if strVal, ok := item.(string); ok {
				trimmedStr := strings.TrimSpace(strVal)

				// Case A: Data URI
				if strings.HasPrefix(trimmedStr, "data:") && strings.Contains(trimmedStr, ";base64,") {
					sub := dataURIRegex.FindStringSubmatch(trimmedStr)
					if len(sub) >= 3 {
						mime := strings.TrimSpace(sub[1])
						if data, ok := tryDecodeBase64(sub[2]); ok && len(data) > 0 {
							ext, detectedMime := detectBinaryExtension(data, mime)
							if ext == "" {
								ext = ".bin"
							}
							if detectedMime == "" {
								detectedMime = mime
							}
							fname := declaredFilename
							if fname == "" {
								fname = fmt.Sprintf("attachment_%d%s", time.Now().UnixNano()/1e6, ext)
							} else {
								fname = sanitizeFilename(fname, ext)
							}
							relPath, err := saveBinaryFile(downloadsDir, wsDir, fname, data)
							if err == nil {
								val[k] = fmt.Sprintf("[Saved to disk: %s (size: %s, type: %s)]", relPath, formatBytes(len(data)), detectedMime)
								val["filePath"] = relPath
								val["saved_path"] = relPath
								val["status"] = "saved_to_disk"
								val["size_bytes"] = len(data)
								*savedFiles = append(*savedFiles, relPath)
								changed = true
								continue
							}
						}
					}
				}

				// Case B: Raw Base64 string in content/data/base64/attachment keys, or large strings
				isBinaryKey := isBinaryPayloadKey(k)
				if (isBinaryKey && len(trimmedStr) >= 64) || len(trimmedStr) >= 512 {
					if data, ok := tryDecodeBase64(trimmedStr); ok && len(data) >= 16 {
						ext, detectedMime := detectBinaryExtension(data, declaredMime)
						// If key is explicitly binary (e.g. content) or ext was detected or data is binary
						if ext != "" || (isBinaryKey && len(data) >= 256) {
							if ext == "" {
								ext = ".bin"
							}
							if detectedMime == "" {
								detectedMime = declaredMime
							}
							if detectedMime == "" {
								detectedMime = "application/octet-stream"
							}

							fname := declaredFilename
							if fname == "" {
								fname = fmt.Sprintf("attachment_%d%s", time.Now().UnixNano()/1e6, ext)
							} else {
								fname = sanitizeFilename(fname, ext)
							}

							relPath, err := saveBinaryFile(downloadsDir, wsDir, fname, data)
							if err == nil {
								val[k] = fmt.Sprintf("[Saved to disk: %s (size: %s, type: %s)]", relPath, formatBytes(len(data)), detectedMime)
								val["filePath"] = relPath
								val["saved_path"] = relPath
								val["status"] = "saved_to_disk"
								val["size_bytes"] = len(data)
								*savedFiles = append(*savedFiles, relPath)
								changed = true
								continue
							}
						}
					}
				}
			}

			// Recursive walk for nested objects
			newSub, subChanged := walkAndInterceptJSON(item, wsDir, downloadsDir, savedFiles)
			if subChanged {
				val[k] = newSub
				changed = true
			}
		}
		return val, changed

	case []any:
		for i, item := range val {
			newElem, subChanged := walkAndInterceptJSON(item, wsDir, downloadsDir, savedFiles)
			if subChanged {
				val[i] = newElem
				changed = true
			}
		}
		return val, changed
	}

	return v, false
}

func isBinaryPayloadKey(k string) bool {
	lower := strings.ToLower(k)
	switch lower {
	case "content", "data", "base64", "blob", "filecontent", "file_content",
		"contentbytes", "content_bytes", "body", "bytes", "attachment", "raw_content":
		return true
	}
	return false
}

func isBase64String(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '+' || r == '/' || r == '=' || r == '\r' || r == '\n' || r == ' ' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func tryDecodeBase64(s string) ([]byte, bool) {
	clean := strings.TrimSpace(s)
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.ReplaceAll(clean, "\n", "")
	clean = strings.ReplaceAll(clean, " ", "")
	clean = strings.ReplaceAll(clean, "\t", "")

	if len(clean) < 16 {
		return nil, false
	}

	if rem := len(clean) % 4; rem != 0 {
		clean += strings.Repeat("=", 4-rem)
	}

	if data, err := base64.StdEncoding.DecodeString(clean); err == nil && len(data) > 0 {
		return data, true
	}
	if data, err := base64.URLEncoding.DecodeString(clean); err == nil && len(data) > 0 {
		return data, true
	}

	return nil, false
}

func detectBinaryExtension(data []byte, declaredMime string) (string, string) {
	if len(data) >= 4 && bytes.HasPrefix(data, []byte("%PDF")) {
		return ".pdf", "application/pdf"
	}
	if len(data) >= 8 && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return ".png", "image/png"
	}
	if len(data) >= 3 && bytes.HasPrefix(data, []byte("\xff\xd8\xff")) {
		return ".jpg", "image/jpeg"
	}
	if len(data) >= 6 && (bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))) {
		return ".gif", "image/gif"
	}
	if len(data) >= 4 && bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		if strings.Contains(declaredMime, "wordprocessingml") || strings.Contains(declaredMime, "docx") {
			return ".docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		}
		if strings.Contains(declaredMime, "spreadsheetml") || strings.Contains(declaredMime, "xlsx") {
			return ".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		}
		return ".zip", "application/zip"
	}
	if len(data) >= 2 && bytes.HasPrefix(data, []byte("\x1f\x8b")) {
		return ".gz", "application/gzip"
	}
	if len(data) >= 6 && bytes.HasPrefix(data, []byte("7z\xbc\xaf\x27\x1c")) {
		return ".7z", "application/x-7z-compressed"
	}
	if len(data) >= 7 && bytes.HasPrefix(data, []byte("Rar!\x1a\x07")) {
		return ".rar", "application/vnd.rar"
	}
	if len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return ".webp", "image/webp"
	}
	if len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return ".wav", "audio/wav"
	}
	if len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("AVI ")) {
		return ".avi", "video/x-msvideo"
	}
	if len(data) >= 3 && (bytes.HasPrefix(data, []byte("ID3")) || (data[0] == 0xff && (data[1]&0xe0) == 0xe0)) {
		return ".mp3", "audio/mpeg"
	}
	if len(data) >= 8 && bytes.Equal(data[4:8], []byte("ftyp")) {
		return ".mp4", "video/mp4"
	}
	if len(data) >= 16 && bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		return ".db", "application/x-sqlite3"
	}
	if len(data) >= 4 && bytes.HasPrefix(data, []byte("\x7fELF")) {
		return ".elf", "application/x-executable"
	}
	if len(data) >= 2 && bytes.HasPrefix(data, []byte("MZ")) {
		return ".exe", "application/x-dosexec"
	}

	switch strings.ToLower(declaredMime) {
	case "application/pdf":
		return ".pdf", declaredMime
	case "image/png":
		return ".png", declaredMime
	case "image/jpeg", "image/jpg":
		return ".jpg", declaredMime
	case "image/webp":
		return ".webp", declaredMime
	case "image/gif":
		return ".gif", declaredMime
	case "application/zip":
		return ".zip", declaredMime
	}

	// High-entropy or binary check: if > 10% non-printable bytes
	nonPrintable := 0
	checkLen := len(data)
	if checkLen > 512 {
		checkLen = 512
	}
	for i := 0; i < checkLen; i++ {
		b := data[i]
		if b == 0 || (b < 9 && b != 0) || (b > 13 && b < 32 && b != 27) {
			nonPrintable++
		}
	}
	if checkLen > 0 && float64(nonPrintable)/float64(checkLen) > 0.10 {
		return ".bin", "application/octet-stream"
	}

	return "", ""
}

func sanitizeFilename(name, defaultExt string) string {
	name = filepath.Base(filepath.FromSlash(name))
	name = strings.ReplaceAll(name, "..", "")
	var clean strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' || r < 32 {
			clean.WriteRune('_')
		} else {
			clean.WriteRune(r)
		}
	}
	s := strings.TrimSpace(clean.String())
	if s == "" || s == "." || s == "_" {
		s = fmt.Sprintf("attachment_%d%s", time.Now().UnixNano()/1e6, defaultExt)
	}
	if defaultExt != "" && !strings.Contains(s, ".") {
		s += defaultExt
	}
	return s
}

func getUniqueFilePath(dir, filename string) (string, string) {
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	candidate := filepath.Join(dir, filename)
	relName := filename
	counter := 1
	for {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, relName
		}
		relName = fmt.Sprintf("%s_%d%s", base, counter, ext)
		candidate = filepath.Join(dir, relName)
		counter++
	}
}

func resolveDownloadsDir(wsDir string) string {
	if wsDir != "" {
		return filepath.Join(wsDir, "downloads")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".unjess", "downloads")
	}
	return "./downloads"
}

func saveBinaryFile(downloadsDir, wsDir, filename string, data []byte) (string, error) {
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		return "", err
	}
	fullPath, relName := getUniqueFilePath(downloadsDir, filename)
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return "", err
	}

	relPath := "downloads/" + relName
	if wsDir != "" {
		if r, err := filepath.Rel(wsDir, fullPath); err == nil && !strings.HasPrefix(r, "..") {
			relPath = filepath.ToSlash(r)
		}
	}
	return relPath, nil
}

func formatBytes(b int) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
