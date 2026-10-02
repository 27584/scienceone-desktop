package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// normalizeArgs accepts either naming convention.
//
// The mainstream harnesses spell these camelCase (oldString, replaceAll,
// maxResults), but a model may also emit snake_case. Rather than picking one and
// failing on the other, both spellings are mirrored onto every key.
func normalizeArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)*2)
	for k, v := range in {
		out[k] = v
	}
	pairs := [][2]string{
		{"oldString", "old_string"},
		{"newString", "new_string"},
		{"replaceAll", "replace_all"},
		{"maxResults", "max_results"},
		{"caseSensitive", "case_sensitive"},
		{"startLine", "start_line"},
		{"endLine", "end_line"},
	}
	for _, p := range pairs {
		a, b := p[0], p[1]
		if _, ok := out[a]; !ok {
			if v, ok := out[b]; ok {
				out[a] = v
			}
		}
		if _, ok := out[b]; !ok {
			if v, ok := out[a]; ok {
				out[b] = v
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// readLines
// ---------------------------------------------------------------------------

// lineWindow resolves the offset/limit pair into a 1-based inclusive range.
// offset counts lines from 1; limit is a count, not an end line.
func lineWindow(args map[string]any, total int) (int, int) {
	start := intArg(args, "offset", 0)
	if start == 0 {
		// Accept the older start_line/end_line spelling as well.
		start = intArg(args, "start_line", 1)
	}
	if start < 1 {
		start = 1
	}
	if start > total {
		start = total
	}

	end := total
	if n := intArg(args, "limit", 0); n > 0 {
		end = start + n - 1
	} else if e := intArg(args, "end_line", 0); e > 0 {
		end = e
	}
	if end > total {
		end = total
	}
	if end < start {
		end = start
	}
	return start, end
}

// readLines returns the file as a structured array rather than a formatted
// block, so the caller can quote exact line numbers back into edit.
func (t *Tools) readLines(args map[string]any) (string, error) {
	rel := strArg(args, "path", "")
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	file, err := t.Sandbox.Resolve(rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no such file or directory: %s", rel)
		}
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, use ls", rel)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(raw), "\n")
	start, end := lineWindow(args, len(lines))

	type line struct {
		N int    `json:"n"`
		T string `json:"text"`
	}
	payload := struct {
		Path      string `json:"path"`
		Total     int    `json:"totalLines"`
		From      int    `json:"from"`
		To        int    `json:"to"`
		Truncated bool   `json:"truncated"`
		Lines     []line `json:"lines"`
	}{
		Path:      t.Sandbox.Rel(file),
		Total:     len(lines),
		From:      start,
		To:        end,
		Truncated: end < len(lines),
	}
	for i := start - 1; i < end; i++ {
		payload.Lines = append(payload.Lines, line{N: i + 1, T: lines[i]})
	}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	hint := ""
	if payload.Truncated {
		hint = fmt.Sprintf("[the file continues to line %d; call readLines again with offset=%d]", len(lines), end+1)
	}
	return truncate(string(b), maxRead, hint), nil
}

// ---------------------------------------------------------------------------
// todoWrite
// ---------------------------------------------------------------------------

type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

var (
	todoMu    sync.Mutex
	todoStore []Todo
)

// todoWrite records the caller's task list.
//
// The list is kept in memory so it survives the platform's per-call
// re-initialisation, and echoed back in a rendered form — the model uses the
// rendering to stay oriented, and the user sees it in the transcript.
func (t *Tools) todoWrite(args map[string]any) (string, error) {
	raw, ok := args["todos"]
	if !ok {
		return "", fmt.Errorf("todos is required")
	}
	items, ok := raw.([]any)
	if !ok {
		return "", fmt.Errorf("todos must be an array")
	}

	var next []Todo
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return "", fmt.Errorf("todos[%d] must be an object", i)
		}
		content := strArg(m, "content", "")
		status := strArg(m, "status", "pending")
		if content == "" {
			return "", fmt.Errorf("todos[%d].content is required", i)
		}
		switch status {
		case "pending", "in_progress", "completed":
		default:
			return "", fmt.Errorf("todos[%d].status must be pending, in_progress or completed", i)
		}
		next = append(next, Todo{Content: content, Status: status})
	}

	todoMu.Lock()
	todoStore = next
	todoMu.Unlock()

	if len(next) == 0 {
		return "task list cleared", nil
	}
	var b strings.Builder
	done, active := 0, 0
	for _, item := range next {
		mark := "[ ]"
		switch item.Status {
		case "completed":
			mark = "[x]"
			done++
		case "in_progress":
			mark = "[~]"
			active++
		}
		fmt.Fprintf(&b, "%s %s\n", mark, item.Content)
	}
	fmt.Fprintf(&b, "\n%d/%d done", done, len(next))
	if active > 1 {
		b.WriteString("  (note: more than one task is in_progress)")
	}
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// pwsh
// ---------------------------------------------------------------------------

// findPowerShell prefers PowerShell 7 when it is installed.
func findPowerShell() string {
	for _, name := range []string{"pwsh", "powershell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// runPwsh executes a PowerShell command.
//
// bash is the default shell here because the model emits POSIX syntax; pwsh
// exists for the Windows-specific corners (registry, services, WMI, .NET) that
// are painful to reach from Git Bash.
func (t *Tools) runPwsh(ctx context.Context, args map[string]any) (string, error) {
	command := strArg(args, "command", "")
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	exe := findPowerShell()
	if exe == "" {
		return "", fmt.Errorf("PowerShell not found on this machine")
	}

	cwd := t.Sandbox.Root
	if c := strArg(args, "cwd", ""); c != "" {
		resolved, err := t.Sandbox.Resolve(c)
		if err != nil {
			return "", err
		}
		cwd = resolved
	}

	seconds := intArg(args, "timeout", 0)
	if seconds <= 0 {
		seconds = int(t.Shell.Timeout / time.Second)
	}
	if seconds > 900 {
		seconds = 900
	}

	cctx, cancel := context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, exe, "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.Dir = cwd
	cmd.Env = shellEnv(t.Shell.ShimDir)

	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	code := 0
	timedOut := cctx.Err() == context.DeadlineExceeded
	switch {
	case timedOut:
		code = 124
	case err != nil:
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 1
			if buf.Len() == 0 {
				buf.WriteString(err.Error())
			}
		}
	}

	note := ""
	if timedOut {
		note = fmt.Sprintf("\n[timed out after %ds and was terminated]", seconds)
	}
	out := buf.String()
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	return truncate(
		fmt.Sprintf("%s%s\nshell=powershell (%s)\n%s", exitTag(code), note, filepath.Base(exe), out),
		maxOutput,
		"[output was too long; re-run with a filter such as `| Select-Object -Last 50`]",
	), nil
}

// exitTag renders a non-zero exit the way the mainstream harnesses do, so the
// model reads it as a result rather than as a tool failure.
func exitTag(code int) string {
	if code == 0 {
		return ""
	}
	return fmt.Sprintf("[exit code: %d]", code)
}
