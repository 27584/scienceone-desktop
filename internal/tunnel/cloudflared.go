// Package tunnel publishes the local MCP server on a public HTTPS address.
//
// The platform can only reach public URLs, and a developer machine sits behind
// NAT, so something has to bridge the two. Rather than reimplement a tunnelling
// protocol, this supervises cloudflared, which is a single static binary and
// needs no account for a quick tunnel.
package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	urlRe   = regexp.MustCompile(`https://[a-zA-Z0-9][a-zA-Z0-9-]*\.trycloudflare\.com`)
	ansiRe  = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	maxLogs = 200
)

// Cloudflared supervises a cloudflared child process and republishes the quick
// tunnel URL whenever it changes.
type Cloudflared struct {
	Exe    string
	Target string

	// OnURL fires whenever the public address becomes known or changes. It runs
	// on the supervisor goroutine, so it must not block.
	OnURL func(string)

	mu       sync.RWMutex
	url      string
	state    string
	since    time.Time
	restarts int
	lastErr  string
	logs     []string
	cmd      *exec.Cmd
}

// FindCloudflared locates the binary: PATH first, then the usual install spots.
func FindCloudflared() string {
	if p, err := exec.LookPath("cloudflared"); err == nil {
		return p
	}
	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = append(candidates,
			`C:\Program Files (x86)\cloudflared\cloudflared.exe`,
			`C:\Program Files\cloudflared\cloudflared.exe`,
		)
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			candidates = append(candidates,
				filepath.Join(local, "Microsoft", "WinGet", "Links", "cloudflared.exe"))
		}
	} else {
		candidates = append(candidates, "/usr/local/bin/cloudflared", "/usr/bin/cloudflared")
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// New builds a tunnel publisher for a local address such as http://127.0.0.1:8801.
func New(target string) *Cloudflared {
	return &Cloudflared{
		Exe:    FindCloudflared(),
		Target: target,
		state:  "stopped",
		logs:   make([]string, 0, maxLogs),
	}
}

// Start launches the tunnel and returns as soon as the process is running; the
// public URL arrives later via OnURL.
func (c *Cloudflared) Start(ctx context.Context) error {
	if c.Exe == "" {
		return fmt.Errorf("cloudflared not found — install it with: winget install Cloudflare.cloudflared")
	}
	c.setState("starting")
	go c.launch(ctx)
	return nil
}

func (c *Cloudflared) launch(ctx context.Context) {
	cmd := exec.CommandContext(ctx, c.Exe,
		"tunnel", "--url", c.Target, "--no-autoupdate")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		c.fail(err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		c.fail(err)
		return
	}

	c.mu.Lock()
	c.cmd = cmd
	c.since = time.Now()
	c.mu.Unlock()

	if err := cmd.Start(); err != nil {
		c.fail(err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.consume(stderr) }()
	go func() { defer wg.Done(); c.consume(stdout) }()

	err = cmd.Wait()
	wg.Wait()

	if ctx.Err() != nil {
		c.setState("stopped")
		return
	}

	// The tunnel dropped on its own. The old address is dead, so clear it
	// before retrying — publishing a stale URL is worse than publishing none.
	c.setURL("")
	c.mu.Lock()
	c.restarts++
	if err != nil {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
	c.setState("retrying")

	select {
	case <-time.After(5 * time.Second):
		c.launch(ctx)
	case <-ctx.Done():
		c.setState("stopped")
	}
}

func (c *Cloudflared) consume(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(ansiRe.ReplaceAllString(scanner.Text(), ""))
		if line == "" {
			continue
		}
		c.appendLog(line)
		if c.URL() == "" {
			if m := urlRe.FindString(line); m != "" {
				c.setURL(m)
			}
		}
	}
}

func (c *Cloudflared) fail(err error) {
	c.mu.Lock()
	c.lastErr = err.Error()
	c.mu.Unlock()
	c.appendLog("error: " + err.Error())
	c.setState("stopped")
}

func (c *Cloudflared) setURL(u string) {
	c.mu.Lock()
	changed := c.url != u
	c.url = u
	if u != "" {
		c.state = "up"
	}
	c.mu.Unlock()
	if changed && u != "" && c.OnURL != nil {
		c.OnURL(u)
	}
}

func (c *Cloudflared) setState(s string) {
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
}

// URL returns the current public address, or "" while it is still being set up.
func (c *Cloudflared) URL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.url
}

// MCPURL is the address the platform should be configured with.
func (c *Cloudflared) MCPURL(path string) string {
	base := c.URL()
	if base == "" {
		return ""
	}
	return strings.TrimSuffix(base, "/") + path
}

// SetOnURL registers the address callback. Call it before Start: the supervisor
// goroutine reads the field without holding the lock.
func (c *Cloudflared) SetOnURL(fn func(string)) {
	c.OnURL = fn
}

func (c *Cloudflared) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Status{
		State:     c.state,
		Provider:  "cloudflared",
		URL:       c.url,
		Target:    c.Target,
		Since:     c.since,
		Restarts:  c.restarts,
		LastError: c.lastErr,
	}
}

func (c *Cloudflared) Logs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.logs))
	copy(out, c.logs)
	return out
}

func (c *Cloudflared) appendLog(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, line)
	if len(c.logs) > maxLogs {
		c.logs = c.logs[len(c.logs)-maxLogs:]
	}
}

// Stop terminates the child process.
func (c *Cloudflared) Stop() {
	c.mu.Lock()
	cmd := c.cmd
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	c.setState("stopped")
}
