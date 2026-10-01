package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// findBash locates Git Bash. The model emits POSIX commands (`||`, `2>&1`,
// heredocs), which PowerShell chokes on, so a POSIX shell is not optional.
func findBash() string {
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("bash"); err == nil {
			return p
		}
		return "/bin/sh"
	}
	candidates := []string{
		`D:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		candidates = append(candidates, filepath.Join(local, "Programs", "Git", "bin", "bash.exe"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// Shell runs commands through Git Bash, normalising Windows path separators on
// the way in and correcting Linux-only mistakes on the way out.
type Shell struct {
	Bash    string
	WorkDir string
	Timeout time.Duration
	ShimDir string // placed first on PATH so python3/pip3 resolve
}

func NewShell(workDir, shimDir string) *Shell {
	return &Shell{Bash: findBash(), WorkDir: workDir, Timeout: 180 * time.Second, ShimDir: shimDir}
}

type ShellResult struct {
	ExitCode int
	Output   string
	TimedOut bool
	Seconds  int
}

var (
	reQuotedAbs = regexp.MustCompile(`"([A-Za-z]):((?:\\[^"]*)+)"`)
	rePlainAbs  = regexp.MustCompile(`([A-Za-z]):((?:\\[^\s"'|&;<>()]+)+)`)
	reRelative  = regexp.MustCompile(`(\.\.?)((?:\\[^\s"'|&;<>()]+)+)`)
	reLinuxCmd  = regexp.MustCompile(`\b(xdg-open|apt-get|apt|sensible-browser|chromium|google-chrome|firefox)\b`)
)

// NormalizeWindowsPaths rewrites Windows separators to forward slashes, which
// both Windows and bash accept. bash treats a backslash as an escape, so
// `D:\Projects\foo` would otherwise arrive as `D:Projectsfoo`.
//
// Only three shapes are touched, so real escape sequences like \n or \t inside
// quoted strings survive.
func NormalizeWindowsPaths(command string) string {
	if runtime.GOOS != "windows" {
		return command
	}
	// The captured remainder always starts with a backslash; drop it so the
	// separator we insert is the only one (".//bin" breaks MSYS ls).
	tail := func(rest string) string {
		s := strings.ReplaceAll(rest, `\`, `/`)
		return strings.TrimLeft(s, "/")
	}
	out := reQuotedAbs.ReplaceAllStringFunc(command, func(m string) string {
		g := reQuotedAbs.FindStringSubmatch(m)
		return `"` + g[1] + `:/` + tail(g[2]) + `"`
	})
	out = rePlainAbs.ReplaceAllStringFunc(out, func(m string) string {
		g := rePlainAbs.FindStringSubmatch(m)
		return g[1] + `:/` + tail(g[2])
	})
	out = reRelative.ReplaceAllStringFunc(out, func(m string) string {
		g := reRelative.FindStringSubmatch(m)
		return g[1] + `/` + tail(g[2])
	})
	return out
}

// Run executes a command and returns its combined output plus the exit code.
func (s *Shell) Run(ctx context.Context, command, cwd string, timeout time.Duration) ShellResult {
	if timeout <= 0 {
		timeout = s.Timeout
	}
	dir := s.WorkDir
	if cwd != "" {
		dir = cwd
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := s.Bash
	if shell == "" {
		shell = "sh"
	}
	cmd := exec.CommandContext(cctx, shell, "-c", NormalizeWindowsPaths(command))
	cmd.Dir = dir
	cmd.Env = shellEnv(s.ShimDir)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	res := ShellResult{Output: buf.String(), Seconds: int(timeout.Seconds())}
	switch {
	case cctx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
		res.ExitCode = 124
	case err != nil:
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = 1
			if res.Output == "" {
				res.Output = err.Error()
			}
		}
	}
	return res
}

// shellEnv makes the child behave as a UTF-8 POSIX shell: the model emits
// Chinese in commands and reads it back from output.
func shellEnv(shimDir string) []string {
	env := os.Environ()
	sep := string(os.PathListSeparator)
	set := map[string]string{
		"LANG":             "C.UTF-8",
		"LC_ALL":           "C.UTF-8",
		"PYTHONIOENCODING": "utf-8",
		"PYTHONUTF8":       "1",
	}
	if shimDir != "" {
		// POSIX names the model likes (python3, pip3) must resolve on Windows.
		set["PATH"] = shimDir + sep + os.Getenv("PATH")
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env
}

// HintFor corrects the "I thought this was Linux" failure in place. A bare
// `command not found` teaches nothing, so the next turn repeats the mistake.
func HintFor(output string, code int) string {
	notFound := code == 127 || strings.Contains(output, "command not found") ||
		strings.Contains(output, "not recognized as an internal")
	if !notFound {
		return ""
	}
	named := reLinuxCmd.FindString(output)
	subject := ""
	if named != "" {
		subject = fmt.Sprintf(" and %q", named)
	}
	return fmt.Sprintf(
		"\n[hint: exit %d%s means the command does not exist here. "+
			"This is a Windows machine, not Linux — there is no xdg-open, apt, chromium or sensible-browser. "+
			"Use the open_path tool to open a file. To run a Windows program, use its full path "+
			"(e.g. /c/Windows/System32/cmd.exe).]", code, subject)
}

// EnvLine travels back with every shell result. The caller routinely assumes
// Linux and burns whole turns before working out where it is.
func EnvLine(workspace string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("env=Windows / Git Bash (MSYS2 MINGW64) / workspace %s", workspace)
	}
	return fmt.Sprintf("env=%s / sh / workspace %s", runtime.GOOS, workspace)
}
