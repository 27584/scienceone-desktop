package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// FrpOptions configures the frp backend.
type FrpOptions struct {
	// LocalPort is the MCP server's port on this machine.
	LocalPort int
	// ServerAddr and ServerPort locate the frps the user runs.
	ServerAddr string
	ServerPort int
	// Token must match auth.token in the server's frps.toml.
	Token string
	// RemotePort is the port on the server that carries this tunnel.
	RemotePort int
	// PublicBase is the https origin sitting in front of that port, e.g.
	// https://mcp.example.com. frp carries TCP only, so TLS is terminated on the
	// user's side, and this is the address the platform is told to fetch.
	// Without it the address falls back to http://<server>:<remotePort>, which
	// the platform will refuse — it wants HTTPS.
	PublicBase string
	// ConfigDir is where frpc.toml is written.
	ConfigDir string
}

// Frp supervises an frpc child process pointed at a server the user runs.
//
// This exists because the zero-configuration backend is not always reachable:
// cloudflared's quick tunnel gives a random address under trycloudflare.com, and
// a platform hosted in mainland China only sometimes gets through to it. frp
// against the user's own server trades setup work for an address that is stable
// and always reachable.
type Frp struct {
	opts FrpOptions
	exe  string

	// OnURL fires whenever the public address becomes known or changes.
	OnURL func(string)

	mu       sync.RWMutex
	url      string
	st       string
	since    time.Time
	restarts int
	lastErr  string
	logs     []string
	cmd      *exec.Cmd
}

// NewFrp builds the backend for the given server. It does not connect yet.
func NewFrp(o FrpOptions) *Frp {
	if o.ConfigDir == "" {
		o.ConfigDir = os.TempDir()
	}
	return &Frp{
		opts: o,
		exe:  FindFrpc(),
		st:   "stopped",
		logs: make([]string, 0, maxLogs),
	}
}

// FindFrpc locates the client binary: PATH first, then beside this executable,
// then the usual install spots.
func FindFrpc() string {
	if p, err := exec.LookPath("frpc"); err == nil {
		return p
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), frpcName()))
	}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			candidates = append(candidates,
				filepath.Join(local, "scienceone-desktop", "bin", frpcName()),
				filepath.Join(local, "Microsoft", "WinGet", "Links", frpcName()),
			)
		}
		candidates = append(candidates, `C:\Program Files\frp\frpc.exe`, `C:\frp\frpc.exe`)
	} else {
		candidates = append(candidates, "/usr/local/bin/frpc", "/usr/bin/frpc")
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func frpcName() string {
	if runtime.GOOS == "windows" {
		return "frpc.exe"
	}
	return "frpc"
}

// Start writes the client config and launches frpc.
func (f *Frp) Start(ctx context.Context) error {
	if f.opts.ServerAddr == "" {
		return errors.New("frp 需要 -frp-server（你的 frps 地址）")
	}
	if f.opts.RemotePort == 0 {
		return errors.New("frp 需要 -frp-remote（服务端上映射的端口）")
	}
	if f.opts.PublicBase == "" {
		return errors.New("frp 需要 -public-url（https 公网地址，平台只接受 HTTPS）")
	}
	if f.exe == "" {
		return fmt.Errorf("找不到 frpc — 从 github.com/fatedier/frp/releases 下载 %s，"+
			"放到程序目录或 %s 后重试", frpcName(), filepath.Join("程序数据目录", "bin"))
	}
	f.setState("starting")
	go f.launch(ctx)
	return nil
}

func (f *Frp) launch(ctx context.Context) {
	cfgPath, err := f.writeConfig()
	if err != nil {
		f.fail(err)
		return
	}

	cmd := exec.CommandContext(ctx, f.exe, "-c", cfgPath)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		f.fail(err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		f.fail(err)
		return
	}

	f.mu.Lock()
	f.cmd = cmd
	f.since = time.Now()
	f.mu.Unlock()

	if err := cmd.Start(); err != nil {
		f.fail(err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); f.consume(stderr) }()
	go func() { defer wg.Done(); f.consume(stdout) }()

	err = cmd.Wait()
	wg.Wait()

	if ctx.Err() != nil {
		f.setState("stopped")
		return
	}

	// The connection dropped. The old address is dead, so clear it before
	// retrying — publishing a stale address is worse than publishing none.
	f.setURL("")
	f.mu.Lock()
	f.restarts++
	if err != nil {
		f.lastErr = err.Error()
	}
	f.mu.Unlock()
	f.setState("retrying")

	select {
	case <-time.After(5 * time.Second):
		f.launch(ctx)
	case <-ctx.Done():
		f.setState("stopped")
	}
}

// writeConfig emits an frpc.toml for a single TCP proxy.
//
// TOML rather than the older ini format because frp v0.52+ reads TOML only, and
// the released binaries are all past that line.
func (f *Frp) writeConfig() (string, error) {
	if err := os.MkdirAll(f.opts.ConfigDir, 0o755); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "serverAddr = %q\n", f.opts.ServerAddr)
	fmt.Fprintf(&b, "serverPort = %d\n", f.opts.ServerPort)
	if f.opts.Token != "" {
		fmt.Fprintf(&b, "auth.token = %q\n", f.opts.Token)
	}
	// Reconnecting beats exiting: the server may be restarting.
	b.WriteString("loginFailExit = false\n")
	b.WriteString("\n[[proxies]]\n")
	b.WriteString("name = \"scienceone-desktop\"\n")
	b.WriteString("type = \"tcp\"\n")
	b.WriteString("localIP = \"127.0.0.1\"\n")
	fmt.Fprintf(&b, "localPort = %d\n", f.opts.LocalPort)
	fmt.Fprintf(&b, "remotePort = %d\n", f.opts.RemotePort)

	path := filepath.Join(f.opts.ConfigDir, "frpc.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (f *Frp) consume(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(ansiRe.ReplaceAllString(scanner.Text(), ""))
		if line == "" {
			continue
		}
		f.appendLog(line)
		if f.URL() == "" && strings.Contains(line, "start proxy success") {
			f.setURL(f.publicURL())
		}
	}
}

// publicURL is the address the platform should be given.
func (f *Frp) publicURL() string {
	return strings.TrimSuffix(f.opts.PublicBase, "/")
}

func (f *Frp) fail(err error) {
	f.mu.Lock()
	f.lastErr = err.Error()
	f.mu.Unlock()
	f.appendLog("error: " + err.Error())
	f.setState("stopped")
}

func (f *Frp) setURL(u string) {
	f.mu.Lock()
	changed := f.url != u
	f.url = u
	if u != "" {
		f.st = "up"
	}
	f.mu.Unlock()
	if changed && u != "" && f.OnURL != nil {
		f.OnURL(u)
	}
}

func (f *Frp) setState(s string) {
	f.mu.Lock()
	f.st = s
	f.mu.Unlock()
}

// URL returns the current public address, or "" while it is still being set up.
func (f *Frp) URL() string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.url
}

// MCPURL is the address the platform should be configured with.
func (f *Frp) MCPURL(path string) string {
	base := f.URL()
	if base == "" {
		return ""
	}
	return strings.TrimSuffix(base, "/") + path
}

// SetOnURL registers the address callback. Call it before Start.
func (f *Frp) SetOnURL(fn func(string)) {
	f.OnURL = fn
}

func (f *Frp) Status() Status {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return Status{
		State:     f.st,
		Provider:  "frp",
		URL:       f.url,
		Target:    fmt.Sprintf("127.0.0.1:%d → %s:%d", f.opts.LocalPort, f.opts.ServerAddr, f.opts.RemotePort),
		Since:     f.since,
		Restarts:  f.restarts,
		LastError: f.lastErr,
	}
}

func (f *Frp) Logs() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, len(f.logs))
	copy(out, f.logs)
	return out
}

func (f *Frp) appendLog(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, line)
	if len(f.logs) > maxLogs {
		f.logs = f.logs[len(f.logs)-maxLogs:]
	}
}

// Stop terminates the child process.
func (f *Frp) Stop() {
	f.mu.Lock()
	cmd := f.cmd
	f.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	f.setState("stopped")
}
