// Command panshi-desktop wraps the ScienceOne (磐石) web app in a desktop shell
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
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yg/panshi-desktop/internal/mcp"
	"github.com/yg/panshi-desktop/internal/tunnel"
	"github.com/yg/panshi-desktop/internal/ui"
)

//go:embed docs/local-dev-mcp.md
var docs embed.FS

const (
	defaultPlatform = "https://ucas.scienceone.cn/lit/#/mcp"
	mcpPath         = "/mcp"
	docName         = "local-dev-mcp.md"
)

type config struct {
	Workspace   string `json:"workspace"`
	Port        int    `json:"port"`
	Token       string `json:"token"`
	PlatformURL string `json:"platformUrl"`
	NoTunnel    bool   `json:"noTunnel"`
}

func main() {
	var (
		workspace = flag.String("workspace", "", "workspace root exposed to the model (default D:\\Projects, or cwd)")
		port      = flag.Int("port", 0, "local MCP port (default 8801)")
		token     = flag.String("token", "", "shared secret (generated on first run and reused)")
		platform  = flag.String("platform", "", "page to wrap")
		noTunnel  = flag.Bool("no-tunnel", false, "serve locally only; do not open a public tunnel")
		headless  = flag.Bool("headless", false, "run the MCP server and tunnel without opening a window")
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
	// Only override the stored setting when the flag was actually passed, so an
	// explicit `-no-tunnel=false` can turn it back on.
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "no-tunnel" {
			cfg.NoTunnel = *noTunnel
		}
	})
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

	tun := tunnel.New("http://" + addr)
	// Record the addresses as soon as they exist: the window is the primary UI,
	// but a file makes the setup scriptable and checkable from outside.
	tun.OnURL = func(u string) {
		writeStatus(cfg, addr, u, tools.Sandbox.Root)
	}
	if !cfg.NoTunnel {
		if err := tun.Start(ctx); err != nil {
			log.Printf("隧道未启动: %v", err)
			log.Printf("仍可使用本机地址 http://%s%s", addr, mcpPath)
		}
	}
	defer tun.Stop()

	fmt.Println()
	fmt.Println("  磐石桌面桥接已启动")
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
			TunnelState: ts.State,
			Workspace:   tools.Sandbox.Root,
			Token:       cfg.Token,
		}
	})

	if *headless {
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
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "panshi-desktop")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".panshi-desktop")
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
