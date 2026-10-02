package ui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

//go:embed bridge.js
var bridgeJS string

// Config is everything the window needs to render and to drive the platform's form.
type Config struct {
	PlatformURL string // the page to wrap
	Workspace   string
	Token       string
	MCPPath     string // path the platform should call, e.g. /mcp
	DocName     string
	DocContent  string
	DataDir     string // WebView2 profile; keeps the login session between runs
}

// Status is the snapshot the injected panel polls for.
type Status struct {
	MCPRunning  bool   `json:"mcpRunning"`
	MCPAddr     string `json:"mcpAddr"`
	MCPURL      string `json:"mcpUrl"`
	ToolCount   int    `json:"toolCount"`
	TunnelURL   string `json:"tunnelUrl"`
	TunnelState string `json:"tunnelState"`
	// TunnelProvider names the backend in use ("cloudflared" or "frp"), so the
	// panel can say where the address came from.
	TunnelProvider string `json:"tunnelProvider"`
	// TunnelWarm is true once the public address has actually answered. A quick
	// tunnel hands out its URL before its edge route is live, and the platform
	// refuses to save an entry whose URL does not answer it.
	TunnelWarm bool   `json:"tunnelWarm"`
	// TunnelWhy explains a tunnel that never became reachable, in the words the
	// transport used. "预热中" on its own left a broken tunnel and a slow one
	// looking identical.
	TunnelWhy string `json:"tunnelWhy"`
	Workspace  string `json:"workspace"`
	Token       string `json:"token"`
}

// App owns the native window and answers calls from the injected script.
type App struct {
	cfg    Config
	status func() Status

	mu   sync.Mutex
	view webview2.WebView
}

func New(cfg Config, status func() Status) *App {
	return &App{cfg: cfg, status: status}
}

// Run blocks on the UI thread until the window is closed.
func (a *App) Run() {
	// The WebView2 message pump must own the thread it was created on.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dataDir := a.cfg.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "scienceone-desktop-webview")
	}
	_ = os.MkdirAll(dataDir, 0o755)

	view := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		// A persistent profile means the platform stays logged in between runs.
		DataPath: dataDir,
		WindowOptions: webview2.WindowOptions{
			Title:  "ScienceOne Desktop Bridge",
			Width:  1360,
			Height: 900,
			Center: true,
		},
	})
	if view == nil {
		fmt.Fprintln(os.Stderr, "无法创建窗口：请确认已安装 Microsoft Edge WebView2 Runtime")
		return
	}
	defer view.Destroy()

	a.mu.Lock()
	a.view = view
	a.mu.Unlock()

	// Init runs before window.onload on every navigation, which is the only
	// point where the panel can be guaranteed to beat the page's own scripts.
	view.Init(bridgeJS)

	if err := view.Bind("scienceoneNative", a.handle); err != nil {
		fmt.Fprintln(os.Stderr, "无法绑定桥接函数:", err)
	}

	view.Navigate(a.cfg.PlatformURL)
	view.Run()
}

// Eval runs JavaScript in the page. Safe to call from any goroutine: the
// WebView2 ExecuteScript call is a COM call and only works on the thread that
// created the controller, so it is marshalled through Dispatch.
func (a *App) Eval(js string) {
	a.mu.Lock()
	view := a.view
	a.mu.Unlock()
	if view == nil {
		return
	}
	view.Dispatch(func() { view.Eval(js) })
}

// Navigate loads a URL in the window. Marshalled for the same reason as Eval.
func (a *App) navigate(url string) {
	a.mu.Lock()
	view := a.view
	a.mu.Unlock()
	if view == nil {
		return
	}
	view.Dispatch(func() { view.Navigate(url) })
}

type nativeReq struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
}

// handle is called from JavaScript. The webview library passes the arguments as
// a JSON array, so the real request is its first element.
func (a *App) handle(argsJSON string) string {
	// Trace every call. A bridge that never fires and a bridge that fires with
	// the wrong payload look identical from the outside otherwise.
	a.trace("call", argsJSON)

	// The library documents a JSON array of the JavaScript arguments, but it
	// actually hands over the single argument as-is. Accept both shapes rather
	// than trusting either.
	raw := strings.TrimSpace(argsJSON)
	if strings.HasPrefix(raw, "[") {
		var parts []string
		if err := json.Unmarshal([]byte(raw), &parts); err == nil && len(parts) > 0 {
			raw = strings.TrimSpace(parts[0])
		}
	}

	var req nativeReq
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		a.trace("error", "malformed request: "+err.Error())
		return errJSON("malformed request: " + err.Error())
	}

	switch req.Action {
	case "status":
		return mustJSON(a.status())
	case "fillConfig":
		return mustJSON(a.fillConfig())
	case "saveProbe":
		return mustJSON(a.saveProbe(req.Payload))
	case "report":
		a.appendReport(req.Payload)
		return `{"ok":true}`
	default:
		return errJSON("unknown action: " + req.Action)
	}
}

// appendReport records what the injected script is doing. Without it, a page the
// script failed to reach looks exactly like one it reached successfully.
func (a *App) appendReport(payload json.RawMessage) {
	a.trace("report", string(payload))
}

// trace appends one line to the bridge log next to the config.
func (a *App) trace(kind, detail string) {
	// Reports carry diagnostic payloads (layout dumps, form values), so they need
	// far more room than the one-line request traces.
	limit := 400
	if kind == "report" {
		limit = 16000
	}
	if len(detail) > limit {
		detail = detail[:limit] + "..."
	}
	f, err := os.OpenFile(a.logPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format("15:04:05"), kind, detail)
}

func (a *App) logPath() string {
	return filepath.Join(filepath.Dir(a.cfg.DataDir), "bridge.log")
}

// fillPayload is exactly what the injected script needs to populate the form.
type fillPayload struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	MCPURL          string `json:"mcpUrl"`
	Headers         string `json:"headers"`
	ProtocolText    string `json:"protocolText"`
	AuthText        string `json:"authText"`
	DocumentName    string `json:"documentName"`
	DocumentContent string `json:"documentContent"`
	// Warm says whether the tunnel has answered at least once, and WarmWhy says
	// why it has not when it has not.
	Warm    bool   `json:"warm"`
	WarmWhy string `json:"warmWhy,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (a *App) fillConfig() fillPayload {
	s := a.status()
	if s.MCPURL == "" {
		// The address is what the platform stores, so without it there is
		// nothing to write. The panel waits and retries; the state and the last
		// tunnel error are surfaced so a persistent failure is diagnosable.
		detail := s.TunnelState
		if detail == "" {
			detail = "unknown"
		}
		return fillPayload{Error: "隧道状态=" + detail}
	}
	return fillPayload{
		// The name field accepts only letters, digits, underscore and hyphen,
		// so it cannot carry a Chinese label.
		Name:            "desktop",
		Description:     "访问本机开发者工作站：浏览工程目录、读写源码、按片段打补丁、正则检索、执行构建与测试命令、用默认程序打开文件。当任务需要读写用户本机上的文件或运行本机命令时才使用本工具；纯文本问答、网页检索、以及ScienceOne自身就能完成的产物生成，优先用平台原生能力。",
		MCPURL:          s.MCPURL,
		Headers:         "X-MCP-Token=" + a.cfg.Token,
		ProtocolText:    "Streamable HTTP",
		AuthText:        "自定义请求头",
		DocumentName:    a.cfg.DocName,
		DocumentContent: a.cfg.DocContent,
		Warm:            s.TunnelWarm,
		WarmWhy:         s.TunnelWhy,
	}
}

// saveProbe writes the page inventory next to the executable so it can be
// inspected when the labels change and the automation needs new anchors.
func (a *App) saveProbe(payload json.RawMessage) map[string]any {
	exe, err := os.Executable()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	dir := filepath.Dir(exe)
	path := filepath.Join(dir, "probe.json")

	var pretty []byte
	var v any
	if err := json.Unmarshal(payload, &v); err == nil {
		pretty, _ = json.MarshalIndent(v, "", "  ")
	} else {
		pretty = payload
	}
	if err := os.WriteFile(path, pretty, 0o644); err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"path": path}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return errJSON(err.Error())
	}
	return string(b)
}

func errJSON(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}
