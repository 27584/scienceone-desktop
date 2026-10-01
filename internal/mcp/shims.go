package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Shim names the model reaches for that do not exist on Windows.
var shimNames = []string{"python3", "python", "pip3", "pip"}

// EnsureShims writes POSIX-named launchers for the interpreter names the model
// emits. Without them `python3 script.py` fails on a Windows box that only has
// `python`, and the caller has no way to know why.
//
// Returns the directory to put first on PATH, or "" when no interpreter was
// found (in which case there is nothing useful to shim).
func EnsureShims(dir string) (string, error) {
	interpreter := findPython()
	if interpreter == "" {
		return "", fmt.Errorf("no Python interpreter found on PATH")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	// Git Bash runs a shebang script with no extension, but the path inside must
	// use forward slashes or the exec fails.
	exe := filepath.ToSlash(interpreter)
	for _, name := range shimNames {
		var body string
		if strings.HasPrefix(name, "pip") {
			body = "#!/bin/sh\nexec \"" + exe + "\" -m pip \"$@\"\n"
		} else {
			body = "#!/bin/sh\nexec \"" + exe + "\" \"$@\"\n"
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// findPython looks for an interpreter the same way a user would have installed one.
func findPython() string {
	if runtime.GOOS != "windows" {
		for _, n := range []string{"python3", "python"} {
			if p, err := exec.LookPath(n); err == nil {
				return p
			}
		}
		return ""
	}

	// `py` is the Windows launcher and is the most reliable way to find the
	// real executable, since `python` may be the Microsoft Store stub.
	if p, err := exec.LookPath("py"); err == nil {
		out, err := exec.Command(p, "-3", "-c", "import sys;print(sys.executable)").Output()
		if err == nil {
			if exe := strings.TrimSpace(string(out)); exe != "" {
				if _, statErr := os.Stat(exe); statErr == nil {
					return exe
				}
			}
		}
	}
	if p, err := exec.LookPath("python"); err == nil {
		if out, err := exec.Command(p, "-c", "import sys;print(sys.executable)").Output(); err == nil {
			if exe := strings.TrimSpace(string(out)); exe != "" {
				if _, statErr := os.Stat(exe); statErr == nil {
					return exe
				}
			}
		}
	}

	// Fall back to the usual install roots.
	globs := []string{
		`D:\Program Files\Python3*\python.exe`,
		`C:\Program Files\Python3*\python.exe`,
		`C:\Python3*\python.exe`,
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		globs = append(globs, filepath.Join(local, `Programs\Python\Python3*\python.exe`))
	}
	for _, g := range globs {
		if matches, err := filepath.Glob(g); err == nil && len(matches) > 0 {
			return matches[len(matches)-1] // the highest version sorts last
		}
	}
	return ""
}
