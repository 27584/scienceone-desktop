package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// find_files
// ---------------------------------------------------------------------------

// globToRegex translates a glob into a regular expression.
//
// filepath.Glob cannot express "**" (any depth), which is the single most useful
// shape when locating a file, so the pattern is compiled by hand: "**" spans
// directories, "*" stays within one segment.
func globToRegex(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString(`(?i)^`)
	runes := []rune(pattern)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch c {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				i++
				// Consume a following separator so "**/x" also matches "x".
				if i+1 < len(runes) && (runes[i+1] == '/' || runes[i+1] == '\\') {
					i++
					b.WriteString(`(?:.*[/\\])?`)
				} else {
					b.WriteString(`.*`)
				}
			} else {
				b.WriteString(`[^/\\]*`)
			}
		case '?':
			b.WriteString(`[^/\\]`)
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteString(`\` + string(c))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`$`)
	return regexp.Compile(b.String())
}

func (t *Tools) findFiles(args map[string]any) (string, error) {
	pattern := strArg(args, "pattern", "")
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	re, err := globToRegex(pattern)
	if err != nil {
		return "", fmt.Errorf("bad pattern: %w", err)
	}
	start, err := t.Sandbox.Resolve(strArg(args, "path", "."))
	if err != nil {
		return "", err
	}
	limit := intArg(args, "max_results", 200)
	if limit < 1 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}

	var found []string
	var walk func(dir string)
	walk = func(dir string) {
		if len(found) >= limit {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(found) >= limit {
				return
			}
			name := e.Name()
			if e.IsDir() {
				if ignoredDirs[name] {
					continue
				}
				walk(filepath.Join(dir, name))
				continue
			}
			full := filepath.Join(dir, name)
			rel := t.Sandbox.Rel(full)
			// Match against the workspace-relative path and the bare name, so
			// both "**/*.py" and "test_*.go" behave as expected.
			if re.MatchString(rel) || re.MatchString(name) {
				found = append(found, rel)
			}
		}
	}
	walk(start)

	if len(found) == 0 {
		return fmt.Sprintf("no files match %q under %s", pattern, t.Sandbox.Rel(start)), nil
	}
	sort.Strings(found)
	body := fmt.Sprintf("%d file(s) matching %s:\n%s", len(found), pattern, strings.Join(found, "\n"))
	return truncate(body, maxOutput, "[narrow the pattern or pass a path]"), nil
}

// ---------------------------------------------------------------------------
// git_status / git_diff
// ---------------------------------------------------------------------------

func (t *Tools) gitRun(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.quotepath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil && text == "" {
		return "", fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

// inRepo reports whether a directory is inside a git working tree. Without this
// check, git prints its entire usage text for a non-repo path, which then looks
// like a successful result.
func (t *Tools) inRepo(ctx context.Context, dir string) bool {
	out, err := t.gitRun(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func (t *Tools) gitStatus(ctx context.Context, args map[string]any) (string, error) {
	dir, err := t.Sandbox.Resolve(strArg(args, "cwd", "."))
	if err != nil {
		return "", err
	}
	if !t.inRepo(ctx, dir) {
		return "", fmt.Errorf("not a git repository: %s", t.Sandbox.Rel(dir))
	}
	branch, _ := t.gitRun(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	status, err := t.gitRun(ctx, dir, "status", "--short", "--untracked-files=all")
	if err != nil {
		return "", fmt.Errorf("git status failed: %w", err)
	}
	body := strings.TrimSpace(status)
	if body == "" {
		body = "(working tree clean)"
	}
	return truncate(
		fmt.Sprintf("branch: %s\n%s", strings.TrimSpace(branch), body),
		maxOutput,
		"[many files changed; use git_diff for one of them]",
	), nil
}

func (t *Tools) gitDiff(ctx context.Context, args map[string]any) (string, error) {
	dir, err := t.Sandbox.Resolve(strArg(args, "cwd", "."))
	if err != nil {
		return "", err
	}
	if !t.inRepo(ctx, dir) {
		return "", fmt.Errorf("not a git repository: %s", t.Sandbox.Rel(dir))
	}
	cmdArgs := []string{"diff", "--no-color"}
	if boolArg(args, "staged", false) {
		cmdArgs = append(cmdArgs, "--cached")
	}
	if rel := strArg(args, "path", ""); rel != "" {
		abs, err := t.Sandbox.Resolve(rel)
		if err != nil {
			return "", err
		}
		cmdArgs = append(cmdArgs, "--", abs)
	}
	out, err := t.gitRun(ctx, dir, cmdArgs...)
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "no differences", nil
	}
	return truncate(out, maxOutput, "[diff is large; pass a single path to narrow it]"), nil
}

// ---------------------------------------------------------------------------
// replace_in_files
// ---------------------------------------------------------------------------

func (t *Tools) replaceInFiles(args map[string]any) (string, error) {
	oldStr := strArg(args, "old_string", "")
	newStr, hasNew := args["new_string"].(string)
	if oldStr == "" || !hasNew {
		return "", fmt.Errorf("old_string and new_string are required")
	}
	if oldStr == newStr {
		return "", fmt.Errorf("old_string and new_string are identical")
	}
	start, err := t.Sandbox.Resolve(strArg(args, "path", "."))
	if err != nil {
		return "", err
	}

	var globRe *regexp.Regexp
	if g := strArg(args, "glob", ""); g != "" {
		if globRe, err = globToRegex(g); err != nil {
			return "", fmt.Errorf("bad glob: %w", err)
		}
	}
	maxFiles := intArg(args, "max_files", 50)
	if maxFiles < 1 {
		maxFiles = 50
	}

	type hit struct {
		rel   string
		count int
	}
	var hits []hit
	var walk func(dir string)
	walk = func(dir string) {
		if len(hits) >= maxFiles {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if len(hits) >= maxFiles {
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
			if err != nil || info.Size() > 4*1024*1024 {
				continue
			}
			data, err := os.ReadFile(full)
			if err != nil || strings.ContainsRune(string(data), 0) {
				continue
			}
			text := string(data)
			n := strings.Count(text, oldStr)
			if n == 0 {
				continue
			}
			if err := os.WriteFile(full, []byte(strings.ReplaceAll(text, oldStr, newStr)), 0o644); err != nil {
				continue
			}
			hits = append(hits, hit{rel: t.Sandbox.Rel(full), count: n})
		}
	}
	walk(start)

	if len(hits) == 0 {
		return fmt.Sprintf("no file contained %q", oldStr), nil
	}
	lines := make([]string, 0, len(hits))
	total := 0
	for _, h := range hits {
		lines = append(lines, fmt.Sprintf("%s (%d)", h.rel, h.count))
		total += h.count
	}
	return fmt.Sprintf("replaced %d occurrence(s) across %d file(s):\n%s",
		total, len(hits), strings.Join(lines, "\n")), nil
}
