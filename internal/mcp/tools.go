package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxOutput = 30000
	maxRead   = 60000
	treeLimit = 800
)

// Tools holds the workspace and shell the handlers operate on.
type Tools struct {
	Sandbox *Sandbox
	Shell   *Shell
	Log     func(event string, data any)
}

func NewTools(root, shimDir string) *Tools {
	sb := NewSandbox(root)
	return &Tools{Sandbox: sb, Shell: NewShell(sb.Root, shimDir), Log: func(string, any) {}}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func strArg(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func boolArg(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

// truncate keeps both ends of an oversized result.
//
// A head-only cut is actively harmful: build and test output puts the verdict
// on the last line, so trimming the tail hides exactly the failure the caller
// asked about. Counting runes rather than bytes avoids splitting UTF-8.
func truncate(text string, limit int, hint string) string {
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	head := limit * 3 / 5
	tail := limit - head
	note := fmt.Sprintf("\n\n[... %d characters omitted from the middle ...]", len(r)-limit)
	if hint != "" {
		note += "\n" + hint
	}
	return string(r[:head]) + note + "\n\n" + string(r[len(r)-tail:])
}

var ignoredDirs = map[string]bool{
	"node_modules": true, ".git": true, ".svn": true, ".hg": true,
	"dist": true, "build": true, "out": true, ".next": true, ".nuxt": true,
	"__pycache__": true, ".venv": true, "venv": true, "target": true,
	".idea": true, ".vscode": true,
}

type dirEntry struct {
	name  string
	isDir bool
}

func readSorted(dir string) ([]dirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]dirEntry, 0, len(entries))
	for _, e := range entries {
		if ignoredDirs[e.Name()] {
			continue
		}
		out = append(out, dirEntry{name: e.Name(), isDir: e.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// ---------------------------------------------------------------------------
// list_dir
// ---------------------------------------------------------------------------

func (t *Tools) listDir(args map[string]any) (string, error) {
	requested := strArg(args, "path", ".")
	dir, err := t.Sandbox.Resolve(requested)
	if err != nil {
		return "", err
	}
	entries, err := readSorted(dir)
	if err != nil {
		return "", t.dirError(requested, err)
	}
	if len(entries) == 0 {
		return "(empty directory)", nil
	}
	depth := intArg(args, "depth", 1)
	if depth < 1 {
		depth = 1
	}
	if depth > 6 {
		depth = 6
	}

	if depth == 1 {
		return renderEntries(entries), nil
	}
	var lines []string
	walkTree(dir, "", depth, &lines)
	if len(lines) == 0 {
		return "(empty directory)", nil
	}
	return strings.Join(lines, "\n"), nil
}

// dirError turns a bare stat failure into something actionable: the caller
// sometimes forwards a whole sentence where a path belongs, and a dead end
// wastes the turn. Handing back the workspace listing lets it aim the retry.
func (t *Tools) dirError(requested string, err error) error {
	if !os.IsNotExist(err) {
		return err
	}
	top := "(unavailable)"
	if entries, e := readSorted(t.Sandbox.Root); e == nil {
		top = renderEntries(entries)
	}
	return fmt.Errorf("no such directory: %s\n"+
		"The workspace root is %s. Paths are relative to it.\n"+
		"Top level of the workspace:\n%s\n"+
		"Call list_dir again with one of those names (or \".\" for the root).",
		requested, t.Sandbox.Root, top)
}

func renderEntries(entries []dirEntry) string {
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.isDir {
			lines = append(lines, e.name+"/")
		} else {
			lines = append(lines, e.name)
		}
	}
	return strings.Join(lines, "\n")
}

func walkTree(dir, indent string, depth int, lines *[]string) {
	if len(*lines) >= treeLimit {
		return
	}
	entries, err := readSorted(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if len(*lines) >= treeLimit {
			*lines = append(*lines, fmt.Sprintf("%s... (stopped at %d entries; narrow the path)", indent, treeLimit))
			return
		}
		if e.isDir {
			*lines = append(*lines, indent+e.name+"/")
			if depth > 1 {
				walkTree(filepath.Join(dir, e.name), indent+"  ", depth-1, lines)
			}
		} else {
			*lines = append(*lines, indent+e.name)
		}
	}
}

// ---------------------------------------------------------------------------
// read_file
// ---------------------------------------------------------------------------

func (t *Tools) readFile(args map[string]any) (string, error) {
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
		return "", fmt.Errorf("%s is a directory, use list_dir", rel)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(raw), "\n")
	total := len(lines)
	start, end := lineWindow(args, total)

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s (lines %d-%d of %d)\n", t.Sandbox.Rel(file), start, end, total)
	for i := start - 1; i < end; i++ {
		fmt.Fprintf(&sb, "%d\t%s\n", i+1, lines[i])
	}

	hint := "[output was cut by size; read a smaller range with offset and limit]"
	if end < total {
		hint = fmt.Sprintf("[the file continues to line %d; call read again with offset=%d]", total, end+1)
	}
	return truncate(sb.String(), maxRead, hint), nil
}

// ---------------------------------------------------------------------------
// write_file
// ---------------------------------------------------------------------------

func (t *Tools) writeFile(args map[string]any) (string, error) {
	rel := strArg(args, "path", "")
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	content, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("content must be a string")
	}
	file, err := t.Sandbox.Resolve(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes (%d lines) to %s",
		len(content), strings.Count(content, "\n")+1, t.Sandbox.Rel(file)), nil
}

// ---------------------------------------------------------------------------
// apply_patch
// ---------------------------------------------------------------------------

func (t *Tools) applyPatch(args map[string]any) (string, error) {
	rel := strArg(args, "path", "")
	oldStr, ok1 := args["old_string"].(string)
	newStr, ok2 := args["new_string"].(string)
	if !ok1 || !ok2 {
		return "", fmt.Errorf("old_string and new_string are required strings")
	}
	if oldStr == newStr {
		return "", fmt.Errorf("old_string and new_string are identical")
	}
	file, err := t.Sandbox.Resolve(rel)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	text := string(raw)
	occurrences := strings.Count(text, oldStr)
	if occurrences == 0 {
		return "", fmt.Errorf("old_string not found. Read the file first and copy the exact text, including indentation")
	}
	replaceAll := boolArg(args, "replace_all", false)
	if occurrences > 1 && !replaceAll {
		return "", fmt.Errorf("old_string appears %d times. Add more surrounding context to make it unique, or set replace_all=true", occurrences)
	}

	next := strings.Replace(text, oldStr, newStr, 1)
	if replaceAll {
		next = strings.ReplaceAll(text, oldStr, newStr)
	}
	if err := os.WriteFile(file, []byte(next), 0o644); err != nil {
		return "", err
	}
	plural := ""
	if occurrences > 1 {
		plural = "s"
	}
	return fmt.Sprintf("patched %s (%d replacement%s)", t.Sandbox.Rel(file), occurrences, plural), nil
}

// ---------------------------------------------------------------------------
// search_code
// ---------------------------------------------------------------------------

func (t *Tools) searchCode(args map[string]any) (string, error) {
	pattern := strArg(args, "pattern", "")
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	flags := "(?i)"
	if boolArg(args, "case_sensitive", false) {
		flags = ""
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return "", fmt.Errorf("bad regular expression: %w", err)
	}

	var globRe *regexp.Regexp
	if g := strArg(args, "glob", ""); g != "" {
		escaped := regexp.QuoteMeta(g)
		escaped = strings.ReplaceAll(escaped, `\*`, `.*`)
		escaped = strings.ReplaceAll(escaped, `\?`, `.`)
		if globRe, err = regexp.Compile(flags + "^" + escaped + "$"); err != nil {
			return "", fmt.Errorf("bad glob: %w", err)
		}
	}

	start, err := t.Sandbox.Resolve(strArg(args, "path", "."))
	if err != nil {
		return "", err
	}
	maxResults := intArg(args, "max_results", 80)
	if maxResults < 1 {
		maxResults = 80
	}
	if maxResults > 300 {
		maxResults = 300
	}

	var results []string
	scanned := 0

	var walk func(dir string)
	walk = func(dir string) {
		if len(results) >= maxResults {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(results) >= maxResults {
				return
			}
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if ignoredDirs[e.Name()] {
					continue
				}
				walk(full)
				continue
			}
			if globRe != nil && !globRe.MatchString(e.Name()) {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() > 2*1024*1024 {
				continue
			}
			data, err := os.ReadFile(full)
			if err != nil || strings.ContainsRune(string(data), 0) {
				continue
			}
			scanned++
			for i, line := range strings.Split(string(data), "\n") {
				if re.MatchString(line) {
					trimmed := strings.TrimSpace(line)
					if len(trimmed) > 240 {
						trimmed = trimmed[:240]
					}
					results = append(results, fmt.Sprintf("%s:%d: %s", t.Sandbox.Rel(full), i+1, trimmed))
					if len(results) >= maxResults {
						return
					}
				}
			}
		}
	}
	walk(start)

	if len(results) == 0 {
		return fmt.Sprintf("no matches for /%s/ (scanned %d files under %s)", pattern, scanned, t.Sandbox.Rel(start)), nil
	}
	body := fmt.Sprintf("%d match(es) for /%s/:\n%s", len(results), pattern, strings.Join(results, "\n"))
	return truncate(body, maxOutput, "[narrow the search with path or glob]"), nil
}

// ---------------------------------------------------------------------------
// run_shell
// ---------------------------------------------------------------------------

func (t *Tools) runShell(ctx context.Context, args map[string]any) (string, error) {
	command := strArg(args, "command", "")
	if command == "" {
		return "", fmt.Errorf("command is required")
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

	res := t.Shell.Run(ctx, command, cwd, time.Duration(seconds)*time.Second)

	note := ""
	if res.TimedOut {
		note = fmt.Sprintf("\n[timed out after %ds and was terminated]"+
			"\n[if it needs longer, raise the timeout argument; if it needs input, it will never finish]", seconds)
	}
	body := res.Output
	if body == "" {
		body = "(no output)"
	}
	out := fmt.Sprintf("%s%s%s\n%s\n%s",
		exitTag(res.ExitCode), note, HintFor(res.Output, res.ExitCode), EnvLine(t.Sandbox.Root), body)
	return truncate(out, maxOutput,
		"[output was too long; re-run with a filter such as `| tail -50` or `-q` to see the middle]"), nil
}

// ---------------------------------------------------------------------------
// open_path
// ---------------------------------------------------------------------------

func (t *Tools) openPath(args map[string]any) (string, error) {
	rel := strArg(args, "path", "")
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	target, err := t.Sandbox.Resolve(rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no such file or directory: %s", rel)
		}
		return "", err
	}
	if strings.Contains(target, `"`) {
		return "", fmt.Errorf("path must not contain a double quote")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// `start` is a cmd builtin, and its first argument is the window title —
		// pass an empty one, or the quoted path is swallowed as that title.
		cmd = exec.Command("cmd", "/c", "start", "", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("could not open %s: %w", t.Sandbox.Rel(target), err)
	}
	go cmd.Wait() //nolint:errcheck // the launcher exits immediately; nothing to report
	return fmt.Sprintf("opened %s with the default application", t.Sandbox.Rel(target)), nil
}

// ---------------------------------------------------------------------------
// ping
// ---------------------------------------------------------------------------

func (t *Tools) ping() string {
	desktop := "not linked"
	if t.Sandbox.HasDesktop() {
		desktop = "available as Desktop/"
	}
	return fmt.Sprintf("pong\nroot=%s\nplatform=%s\ndesktop=%s\nnode=go/%s",
		t.Sandbox.Root, runtime.GOOS, desktop, runtime.Version())
}
