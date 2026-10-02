// Command scienceone-desktop wraps the ScienceOne (磐石) web app in a desktop shell
// and wires the machine it runs on into that app as an MCP tool server.
//
// One process does the whole job:
//
//	MCP server   exposes the workspace (files, shell, open) on 127.0.0.1
//	tunnel       publishes that port on a public HTTPS address, because the
//	             platform can only reach public URLs and this machine is behind NAT
//	window       loads the platform page and injects a panel that can fill the
//	             MCP form automatically, so nothing has to be copied by hand
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yg/scienceone-desktop/internal/mcp"
	"github.com/yg/scienceone-desktop/internal/tunnel"
	"github.com/yg/scienceone-desktop/internal/ui"
)

//go:embed docs/local-dev-mcp.md
var docs embed.FS

const (
	defaultPlatform = "https://ucas.scienceone.cn/lit/#/mcp"
	mcpPath         = "/mcp"
	docName         = "local-dev-mcp.md"
)

type config struct {
	Workspace   string       `json:"workspace"`
	Port        int          `json:"port"`
	Token       string       `json:"token"`
	PlatformURL string       `json:"platformUrl"`
	Tunnel      tunnelConfig `json:"tunnel"`
}

// tunnelConfig selects and configures the public-address backend.
//
// The default (empty provider) is cloudflared: no account, no setup, but a
// random address under trycloudflare.com that a mainland-China platform reaches
// only some of the time. Setting Provider to "frp" swaps in the user's own
// server, which is more work once and reliable afterwards.
type tunnelConfig struct {
	Provider string `json:"provider"`
	// frp: where the user's frps listens, and the token it expects.
	ServerAddr string `json:"serverAddr"`
	ServerPort int    `json:"serverPort"`
	FrpToken   string `json:"frpToken"`
	// frp: the port on the server that carries this tunnel.
	RemotePort int `json:"remotePort"`
	// PublicURL is the https origin in front of that port. frp moves TCP only,
	// so TLS lives on the user's side; the platform is given this address.
	PublicURL string `json:"publicUrl"`
}

// runtime holds per-run switches. These are deliberately not persisted: a
// debugging flag such as -no-tunnel must not survive into the next launch, or
// the tunnel silently never starts again.
type runtimeOpts struct {
	noTunnel bool
	headless bool
}

func main() {
	var (
		workspace = flag.String("workspace", "", "workspace root exposed to the model (default D:\\Projects, or cwd)")
		port      = flag.Int("port", 0, "local MCP port (default 8801)")
		token     = flag.String("token", "", "shared secret (generated on first run and reused)")
		platform  = flag.String("platform", "", "page to wrap")
		noTunnel  = flag.Bool("no-tunnel", false, "serve locally only; do not open a public tunnel")
		headless  = flag.Bool("headless", false, "run the MCP server and tunnel without opening a window")

		tunnelKind = flag.String("tunnel", "", "tunnel backend: cloudflared (default) or frp")
		frpServer  = flag.String("frp-server", "", "frp only: your frps address")
		frpPort    = flag.Int("frp-port", 0, "frp only: frps port (default 7000)")
		frpToken   = flag.String("frp-token", "", "frp only: auth token, must match frps.toml")
		frpRemote  = flag.Int("frp-remote", 0, "frp only: port on the server that carries this tunnel")
		publicURL  = flag.String("public-url", "", "https address the platform will reach, in front of that port")
	)
	flag.Parse()

	cfg := loadConfig()
	if *workspace != "" {
		cfg.Workspace = *workspace
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *token != "" {
		cfg.Token = *token
	}
	if *platform != "" {
		cfg.PlatformURL = *platform
	}
	if *tunnelKind != "" {
		cfg.Tunnel.Provider = *tunnelKind
	}
	if *frpServer != "" {
		cfg.Tunnel.ServerAddr = *frpServer
	}
	if *frpPort != 0 {
		cfg.Tunnel.ServerPort = *frpPort
	}
	if *frpToken != "" {
		cfg.Tunnel.FrpToken = *frpToken
	}
	if *frpRemote != 0 {
		cfg.Tunnel.RemotePort = *frpRemote
	}
	if *publicURL != "" {
		cfg.Tunnel.PublicURL = *publicURL
	}
	opts := runtimeOpts{noTunnel: *noTunnel, headless: *headless}
	if cfg.Workspace == "" {
		if _, err := os.Stat(`D:\Projects`); err == nil {
			cfg.Workspace = `D:\Projects`
		} else if wd, err := os.Getwd(); err == nil {
			cfg.Workspace = wd
		}
	}
	if cfg.Port == 0 {
		cfg.Port = 8801
	}
	if cfg.Token == "" {
		cfg.Token = randomToken()
	}
	if cfg.PlatformURL == "" {
		cfg.PlatformURL = defaultPlatform
	}
	saveConfig(cfg)

	docBytes, err := docs.ReadFile("docs/" + docName)
	if err != nil {
		log.Fatalf("内置说明文档缺失: %v", err)
	}

	// The model writes "Desktop/foo.html" when the user says "on my desktop",
	// so the workspace needs a doorway to the real one.
	linkDesktop(cfg.Workspace)

	// python3 / pip3 do not exist on Windows; the model emits them anyway.
	shimDir, err := mcp.EnsureShims(filepath.Join(configDir(), "bin"))
	if err != nil {
		log.Printf("未创建 python 命令别名: %v", err)
		shimDir = ""
	}

	tools := mcp.NewTools(cfg.Workspace, shimDir)
	srv := mcp.NewServer(tools, cfg.Token)
	// The window is the primary UI, but this process usually runs without a
	// console, so its events need a file to be diagnosable at all.
	mcpLog := filepath.Join(configDir(), "mcp.log")
	_ = os.Remove(mcpLog)
	srv.Log = func(event string, data any) {
		appendLog(mcpLog, event, data)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("无法监听 %s: %v（端口被占用？用 -port 换一个）", addr, err)
	}
	httpSrv := &http.Server{Handler: srv}
	go func() {
		if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("MCP 服务退出: %v", err)
		}
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tun := newTunnel(cfg, addr, configDir())
	// A public address is not usable the instant it is known — a quick tunnel in
	// particular hands one out before its route exists — so that fact is tracked
	// here and the panel waits for it before filling in a form the platform is
	// going to test.
	var tunnelWarm atomic.Bool
	var tunnelWhy atomic.Value
	tunnelWhy.Store("")
	// Record the addresses as soon as they exist: the window is the primary UI,
	// but a file makes the setup scriptable and checkable from outside.
	tun.SetOnURL(func(u string) {
		writeStatus(cfg, addr, u, tools.Sandbox.Root)
		go func() {
			tunnelWarm.Store(false)
			tunnelWhy.Store("正在探测…")
			ok, why := warmup(strings.TrimSuffix(u, "/")+mcpPath, cfg.Token)
			tunnelWarm.Store(ok)
			if ok {
				tunnelWhy.Store("")
			} else {
				tunnelWhy.Store(why)
			}
		}()
	})
	if !opts.noTunnel {
		if err := tun.Start(ctx); err != nil {
			log.Printf("隧道未启动: %v", err)
			log.Printf("仍可使用本机地址 http://%s%s", addr, mcpPath)
		}
	}
	defer tun.Stop()

	fmt.Println()
	fmt.Println("  ScienceOne桌面桥接已启动")
	fmt.Printf("    MCP 服务   http://%s%s\n", addr, mcpPath)
	fmt.Printf("    工作目录   %s\n", tools.Sandbox.Root)
	fmt.Printf("    令牌       %s\n", cfg.Token)
	fmt.Println("    公网地址   建立中，窗口右上角的面板会显示")
	fmt.Println()

	app := ui.New(ui.Config{
		PlatformURL: cfg.PlatformURL,
		Workspace:   tools.Sandbox.Root,
		Token:       cfg.Token,
		MCPPath:     mcpPath,
		DocName:     docName,
		DocContent:  string(docBytes),
		DataDir:     filepath.Join(configDir(), "webview"),
	}, func() ui.Status {
		ts := tun.Status()
		mcpURL := ""
		if ts.URL != "" {
			mcpURL = strings.TrimSuffix(ts.URL, "/") + mcpPath
		}
		return ui.Status{
			MCPRunning:  true,
			MCPAddr:     addr,
			MCPURL:      mcpURL,
			ToolCount:   len(mcp.ToolList()),
			TunnelURL:   ts.URL,
			TunnelState:    ts.State,
			TunnelProvider: ts.Provider,
			TunnelWarm:     tunnelWarm.Load(),
			TunnelWhy:      fmt.Sprint(tunnelWhy.Load()),
			Workspace:   tools.Sandbox.Root,
			Token:       cfg.Token,
		}
	})

	if opts.headless {
		// Useful for verifying the service, and for running the bridge on a
		// machine that is only ever driven remotely.
		<-ctx.Done()
	} else {
		// A loopback control endpoint makes the injected automation drivable
		// without a mouse, which is how the form filling is tested.
		controlAddr := fmt.Sprintf("127.0.0.1:%d", cfg.Port+1)
		control := &http.Server{Addr: controlAddr, Handler: &ui.Control{App: app, Token: cfg.Token}}
		go func() {
			if err := control.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("控制接口未启动: %v", err)
			}
		}()
		defer func() {
			sctx, done := context.WithTimeout(context.Background(), 2*time.Second)
			defer done()
			_ = control.Shutdown(sctx)
		}()
		fmt.Printf("    控制接口   http://%s/control/ready\n", controlAddr)

		app.Run() // blocks until the window closes
	}

	shutdownCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	_ = httpSrv.Shutdown(shutdownCtx)
	fmt.Println("已退出。")
}

// newTunnel builds the backend the configuration asks for.
//
// The default is cloudflared because it needs nothing from the user. frp is
// selected explicitly, because it needs a server — and once that server exists
// the address it gives is stable and always reachable, which the quick tunnel
// never is.
func newTunnel(cfg config, addr, configDir string) tunnel.Tunnel {
	if cfg.Tunnel.Provider == "frp" {
		port := cfg.Tunnel.ServerPort
		if port == 0 {
			port = 7000
		}
		return tunnel.NewFrp(tunnel.FrpOptions{
			LocalPort:  cfg.Port,
			ServerAddr: cfg.Tunnel.ServerAddr,
			ServerPort: port,
			Token:      cfg.Tunnel.FrpToken,
			RemotePort: cfg.Tunnel.RemotePort,
			PublicBase: cfg.Tunnel.PublicURL,
			ConfigDir:  configDir,
		})
	}
	return tunnel.New("http://" + addr)
}

// linkDesktop creates a junction from the workspace to the user's real desktop
// so "put it on my desktop" works. A junction is used rather than a symlink
// because it needs no elevation on Windows.
func linkDesktop(workspace string) {
	link := filepath.Join(workspace, "Desktop")
	if _, err := os.Stat(link); err == nil {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	desktop := filepath.Join(home, "Desktop")
	if _, err := os.Stat(desktop); err != nil {
		return
	}
	cmd := exec.Command("cmd", "/c", "mklink", "/J", link, desktop)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("未能链接桌面（可手动创建）: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// publicResolvers are consulted when the system resolver returns nothing.
//
// On the machine this was developed on, *.trycloudflare.com came back NXDOMAIN
// from the local resolver (instantly, so it was not a timeout) while 223.5.5.5
// and 8.8.8.8 both resolved it normally — the tunnel was fine and the probe was
// reporting it broken. Falling back makes the probe answer the question it is
// actually asked: can this tunnel be reached at all.
var publicResolvers = []string{"223.5.5.5:53", "119.29.29.29:53", "8.8.8.8:53"}

// dialResolvingThroughPublicDNS dials addr, resolving the host through the
// system first and through public resolvers if that yields nothing. The original
// hostname is kept for SNI, so certificate validation is unaffected.
func dialResolvingThroughPublicDNS(ctx context.Context, network, addr string) (net.Conn, error) {
	dial := func(target string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, target)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return dial(addr)
	}
	if _, err := net.DefaultResolver.LookupHost(ctx, host); err == nil {
		return dial(addr)
	}

	for _, server := range publicResolvers {
		r := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, netw, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, netw, server)
			},
		}
		addrs, err := r.LookupHost(ctx, host)
		if err != nil || len(addrs) == 0 {
			continue
		}
		var lastErr error
		for _, a := range addrs {
			conn, err := dial(net.JoinHostPort(a, port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
	}
	return dial(addr)
}

// warmup pokes the public URL until it actually answers, and reports why not
// when it never does.
//
// A quick tunnel hands out its address before its edge route exists, so early
// requests time out and that is expected. Two things are not expected and are
// worth reporting instead of retrying silently:
//
//   - the name does not resolve at all, which means the edge registration never
//     took — no amount of waiting fixes that, and the panel is better told;
//   - anything else the transport says, which is what distinguishes "the network
//     is slow" from "this address will never work".
//
// The timeout is short and the retries close together on purpose: the goal is to
// notice the moment the route comes up, not to wait out a long timeout on a
// route that is not there.
func warmup(url, token string) (bool, string) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: dialResolvingThroughPublicDNS,
		},
	}
	last := "no attempt got through"
	dnsFails := 0

	for i := 0; i < 400; i++ {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err == nil {
			req.Header.Set("X-MCP-Token", token)
			resp, err := client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return true, ""
				}
				last = fmt.Sprintf("the edge answered HTTP %d", resp.StatusCode)
				dnsFails = 0
			} else {
				last = err.Error()
				if strings.Contains(last, "no such host") {
					dnsFails++
					if dnsFails >= 5 {
						return false, "域名解析失败（隧道未在边缘注册成功）：" + last
					}
				} else {
					dnsFails = 0
				}
			}
		}
		time.Sleep(800 * time.Millisecond)
	}
	return false, last
}

// appendLog adds one line to the MCP event log. Every request the platform
// makes lands here, including the ones it rejects before speaking MCP, which is
// what makes a "URL unreachable" verdict from the platform diagnosable.
func appendLog(path, event string, data any) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(data)
	if err != nil {
		line = []byte(fmt.Sprint(data))
	}
	fmt.Fprintf(f, "%s %-14s %s\n", time.Now().Format("15:04:05"), event, line)
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// writeStatus drops the addresses the platform needs into a file, so the setup
// can be scripted or inspected without reading the window.
func writeStatus(cfg config, addr, tunnelURL, workspace string) {
	doc := map[string]any{
		"localUrl":  "http://" + addr + mcpPath,
		"tunnelUrl": tunnelURL,
		"mcpUrl":    strings.TrimSuffix(tunnelURL, "/") + mcpPath,
		"token":     cfg.Token,
		"workspace": workspace,
		"updatedAt": time.Now().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	dir := configDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "status.json"), data, 0o644)
}

func configDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	// An earlier build kept its state under a different name. Reuse that
	// directory instead of migrating: it holds the WebView2 profile, the profile
	// holds the platform login, and copying a profile of tens of thousands of
	// files is both slow and easy to get wrong — while losing it would force the
	// user to sign in again after an upgrade.
	if legacy := filepath.Join(base, "panshi-desktop"); dirExists(legacy) {
		return legacy
	}
	return filepath.Join(base, "scienceone-desktop")
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func loadConfig() config {
	var cfg config
	path := filepath.Join(configDir(), "config.json")
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	return cfg
}

func saveConfig(cfg config) {
	dir := configDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644)
}
