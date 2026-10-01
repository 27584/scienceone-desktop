package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeJunction links a directory without needing elevation, which a symlink
// would on Windows.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		return
	}
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("junction unavailable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// The link must be removed before the parent directory, or the cleanup
	// walk would descend into the target.
	t.Cleanup(func() { _ = os.Remove(link) })
}

func TestResolveRefusesToEscape(t *testing.T) {
	root := t.TempDir()
	sb := NewSandbox(root)

	for _, rel := range []string{
		"..",
		"../..",
		"../../Windows/System32/drivers/etc/hosts",
		"a/b/../../../..",
		`..\..\Windows`,
	} {
		if _, err := sb.Resolve(rel); err == nil {
			t.Errorf("Resolve(%q) should have been refused", rel)
		}
	}
}

func TestResolveAllowsInsideAndNewPaths(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "a", "b"))
	mustWrite(t, filepath.Join(root, "a", "b", "c.txt"), "hi")
	sb := NewSandbox(root)

	got, err := sb.Resolve("a/b/c.txt")
	if err != nil {
		t.Fatalf("existing file should resolve: %v", err)
	}
	if filepath.Base(got) != "c.txt" {
		t.Errorf("resolved to %q, expected .../c.txt", got)
	}

	// write_file creates files, so a path whose tail does not exist yet has to
	// resolve as long as its parent is inside the workspace.
	if _, err := sb.Resolve("new/dir/file.txt"); err != nil {
		t.Errorf("a not-yet-existing path inside the root should resolve: %v", err)
	}
}

// A junction inside the workspace must not become a way out of it.
func TestJunctionCannotClimbOut(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "work")
	outside := filepath.Join(base, "outside")
	mustMkdir(t, root)
	mustMkdir(t, outside)
	mustWrite(t, filepath.Join(outside, "secret.txt"), "secret")

	makeJunction(t, filepath.Join(root, "link"), outside)
	sb := NewSandbox(root)

	if _, err := sb.Resolve("link/secret.txt"); err == nil {
		t.Error("reading through an unregistered junction must be refused")
	}
	if _, err := sb.Resolve("link/.."); err == nil {
		t.Error("climbing out through a junction must be refused")
	}
}

// The desktop link is the one junction that is followed, and only inwards.
func TestDesktopLinkIsFollowedButNotClimbable(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	desktop := filepath.Join(home, "Desktop")
	if _, err := os.Stat(desktop); err != nil {
		t.Skip("no desktop directory")
	}

	root := t.TempDir()
	makeJunction(t, filepath.Join(root, "Desktop"), desktop)
	sb := NewSandbox(root)

	if !sb.HasDesktop() {
		t.Fatal("the desktop junction should have been detected")
	}
	if _, err := sb.Resolve("Desktop"); err != nil {
		t.Errorf("the desktop itself should resolve: %v", err)
	}
	if _, err := sb.Resolve("Desktop/.."); err == nil {
		t.Error("Desktop/.. must be refused: it lands in the user profile")
	}
	if _, err := sb.Resolve("Desktop/../../.ssh"); err == nil {
		t.Error("walking from the desktop into the profile must be refused")
	}
}

func TestRelRendersThroughTheLinkName(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	desktop := filepath.Join(home, "Desktop")
	if _, err := os.Stat(desktop); err != nil {
		t.Skip("no desktop directory")
	}
	root := t.TempDir()
	makeJunction(t, filepath.Join(root, "Desktop"), desktop)
	sb := NewSandbox(root)

	got := sb.Rel(filepath.Join(desktop, "note.txt"))
	if got != "Desktop/note.txt" {
		t.Errorf("Rel = %q, want Desktop/note.txt", got)
	}
}

func TestNormalizeWindowsPaths(t *testing.T) {
	cases := []struct{ in, want string }{
		{`ls "D:\Projects\AI Projects"`, `ls "D:/Projects/AI Projects"`},
		{`cat D:\Projects\foo.txt`, `cat D:/Projects/foo.txt`},
		{`ls .\bin`, `ls ./bin`},
		{`ls ..\..\Windows`, `ls ../../Windows`},
		// Real escape sequences inside quoted strings must survive.
		{`printf "a\nb"`, `printf "a\nb"`},
		{`echo "no path here"`, `echo "no path here"`},
	}
	if runtime.GOOS != "windows" {
		t.Skip("path rewriting is a Windows concern")
	}
	for _, c := range cases {
		if got := NormalizeWindowsPaths(c.in); got != c.want {
			t.Errorf("NormalizeWindowsPaths(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHintOnlyFiresOnMissingCommands(t *testing.T) {
	if h := HintFor("everything is fine", 0); h != "" {
		t.Errorf("a successful command should get no hint, got %q", h)
	}
	h := HintFor("/usr/bin/bash: xdg-open: command not found", 127)
	if !strings.Contains(h, "not Linux") || !strings.Contains(h, "xdg-open") {
		t.Errorf("a missing Linux command should be corrected, got %q", h)
	}
}

func TestTruncateKeepsBothEnds(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	out := truncate(sb.String(), 2000, "")
	if !strings.Contains(out, "line 0") {
		t.Error("the start should survive truncation")
	}
	if !strings.Contains(out, "line 4999") {
		t.Error("the end should survive truncation: failures appear there")
	}
	if !strings.Contains(out, "omitted from the middle") {
		t.Error("the omission should be marked")
	}
}
