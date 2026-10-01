package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Link is a named doorway inside the workspace that resolves somewhere else on
// disk. The desktop is the one that matters in practice: the user says "put it
// on my desktop", and refusing that would make the tool useless.
type Link struct {
	Name string // the name as the caller sees it, e.g. "Desktop"
	Path string // the real directory it resolves to
}

// Sandbox confines every file operation to the workspace root, plus a small set
// of explicitly linked directories.
type Sandbox struct {
	Root  string
	Links []Link
	base  string // realpath of Root, cached
}

// NewSandbox resolves the root and picks up a "Desktop" link when the workspace
// contains one pointing at the user's real desktop.
func NewSandbox(root string) *Sandbox {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	s := &Sandbox{Root: abs}
	// Stored normalised: `allowed` compares it against a normalised path, and on
	// Windows that means lower-case, or every prefix test fails.
	s.base = normPath(realpathBestEffort(abs))

	// Pick up a "Desktop" link when the workspace contains one pointing at the
	// user's real desktop. A plain directory of that name is left alone.
	linkPath := filepath.Join(abs, "Desktop")
	linkReal := realpathBestEffort(linkPath)
	if normPath(linkReal) != normPath(linkPath) {
		if home, err := os.UserHomeDir(); err == nil {
			if normPath(linkReal) == normPath(realpathBestEffort(filepath.Join(home, "Desktop"))) {
				s.Links = append(s.Links, Link{Name: "Desktop", Path: linkReal})
			}
		}
	}
	return s
}

// HasDesktop reports whether the desktop link is wired up.
func (s *Sandbox) HasDesktop() bool {
	for _, l := range s.Links {
		if l.Name == "Desktop" {
			return true
		}
	}
	return false
}

// Resolve turns a caller-supplied relative path into a real absolute path,
// refusing anything that lands outside the sandbox.
func (s *Sandbox) Resolve(rel string) (string, error) {
	real := s.resolveThrough(s.Root, rel)
	if !s.allowed(real) {
		return "", fmt.Errorf("path escapes workspace root: %s", rel)
	}
	return real, nil
}

// resolveThrough walks the path one segment at a time, resolving each step
// through the filesystem before applying the next.
//
// Collapsing the string first is wrong: filepath.Join(root, "Desktop/..")
// folds to the root before the link is ever followed, which would let a link be
// used to climb out. Applying ".." to the *resolved* location closes that hole.
func (s *Sandbox) resolveThrough(base, rel string) string {
	segments := strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' })
	current := realpathBestEffort(base)
	for _, segment := range segments {
		switch segment {
		case ".", "":
			continue
		case "..":
			current = filepath.Dir(realpathBestEffort(current))
		default:
			current = filepath.Join(realpathBestEffort(current), segment)
		}
	}
	return current
}

func (s *Sandbox) allowed(real string) bool {
	n := normPath(real)
	if within(n, s.base) {
		return true
	}
	for _, l := range s.Links {
		if within(n, normPath(l.Path)) {
			return true
		}
	}
	return false
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// Rel renders an absolute path the way the caller should see it: through the
// link name it used (Desktop/foo.html), not the resolved location on disk.
func (s *Sandbox) Rel(abs string) string {
	for _, l := range s.Links {
		if rel, err := filepath.Rel(l.Path, abs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return l.Name + "/" + filepath.ToSlash(rel)
		}
	}
	rel, err := filepath.Rel(s.Root, abs)
	if err != nil {
		return abs
	}
	if rel == "." {
		return "."
	}
	return filepath.ToSlash(rel)
}

// realpathBestEffort resolves symlinks and junctions, tolerating a tail that
// does not exist yet (write_file creates files).
//
// filepath.EvalSymlinks is not usable here: on Windows it follows symlinks but
// reports a directory junction as an ordinary directory, so a junction inside
// the workspace would look like a plain folder and could be walked out of with
// "..". os.Readlink does report the junction target, so links are resolved one
// path component at a time instead.
func realpathBestEffort(target string) string {
	abs, err := filepath.Abs(target)
	if err != nil {
		return target
	}
	volume := filepath.VolumeName(abs)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.Trim(filepath.ToSlash(abs[len(volume):]), "/"), "/") {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		// A chain of links is possible; bound the walk so a cycle cannot hang.
		for i := 0; i < 16; i++ {
			link, err := os.Readlink(current)
			if err != nil {
				break
			}
			if !filepath.IsAbs(link) {
				link = filepath.Join(filepath.Dir(current), link)
			}
			current = filepath.Clean(link)
		}
	}
	return current
}

func normPath(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}
