# ScienceOne Desktop Bridge

把本机变成 **ScienceOne（磐石）** 能直接调用的开发工作站。

ScienceOne 网页端不提供对外开放的 API，但它支持 **MCP**（Model Context Protocol）。
这个程序把三件原本需要手工拼装的事合成一个可执行文件：

1. 在本机起一个 MCP 服务，把工作目录暴露成一整套文件与 shell 工具；
2. 用 [cloudflared](https://github.com/cloudflare/cloudflared) 把那个服务发布成一个公网 HTTPS 地址——平台只能访问公网地址，而开发机在 NAT 后面；
3. 把 ScienceOne 网页装进桌面窗口，并注入一个面板，一键把地址、令牌和说明文档填进平台的 MCP 表单。

结束后，你在 ScienceOne 对话框里说"把 `MyZone` 里的登录 bug 修了"，模型真的能读到本机文件、改代码、跑测试——而不是只给你一段让你自己粘贴的代码。

---

## 30 秒开始

**前置**：Windows 10/11、[WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)（Win11 已内置）、[cloudflared](https://github.com/cloudflare/cloudflared/releases)。

```
winget install Cloudflare.cloudflared
```

**启动**：

```bat
启动.cmd
```

窗口打开后，右上角有一个折叠面板。第一次使用：

1. 在窗口里登录 ScienceOne；
2. 展开面板，确认「工作目录」是你想交给模型的那个目录；
3. 等「公网地址」变成一条 `https://xxx.trycloudflare.com`；
4. 点 **一键填入 ScienceOne 表单**。

面板会自己导航到 `MCP 工具` 页，创建（或更新）一条名为 `desktop` 的 MCP 条目，填好 URL、`X-MCP-Token` 请求头、学科和说明文档。

最后一步目前需要手动确认：回到对话页，在输入框的工具菜单里把 `desktop` 打开。之后模型就能调用本机了。

---

## 命令行

```
scienceone-desktop.exe [flags]

-workspace <dir>   暴露给模型的工作目录（默认 D:\Projects，否则用当前目录）
-port <n>          本机 MCP 端口（默认 8801）
-token <str>       共享令牌（首次运行随机生成并复用）
-platform <url>    要包装的页面（默认 https://ucas.scienceone.cn/lit/#/mcp）
-no-tunnel         只在本机提供服务，不开公网隧道
-headless          只跑 MCP 服务和隧道，不开窗口

-tunnel <kind>     隧道后端：cloudflared（默认）或 frp
-frp-server <host> frp：你的 frps 地址
-frp-port <n>      frp：frps 端口（默认 7000）
-frp-token <str>   frp：鉴权令牌，与服务端 frps.toml 一致
-frp-remote <n>    frp：服务端上承载隧道的端口
-public-url <url>  frp：前端的 https 地址——平台实际访问的就是它
```

后六项只在 `-tunnel frp` 时使用，配置一次就写进 `config.json`。完整步骤见 [deploy/frps.md](deploy/frps.md)。

`-no-tunnel` 和 `-headless` 是调试开关，**不会写进配置**——否则一次调试启动会让之后的每次启动都悄悄失去隧道。

工作目录、端口、令牌、平台地址存在：

```
%LOCALAPPDATA%\scienceone-desktop\config.json
```

同一个目录下还有 `status.json`（当前公网地址）、`bridge.log`（面板自动化日志）、`mcp.log`（平台发来的每一次请求）。

---

## 工具

模型看到的 12 个工具，命名与参数沿用主流编码工具链的约定，模型不需要被教第二遍。

| 工具 | 作用 |
|---|---|
| `ls` | 列目录，`depth` 可一次展开整棵工程树 |
| `read` | 读文件，带行号，支持 `offset` / `limit` |
| `readLines` | 读文件，返回带精确行号的结构化数组 |
| `write` | 写完整文件，父目录自动创建 |
| `edit` | 按精确片段替换，改已有代码的首选 |
| `glob` | 按路径模式找文件 |
| `grep` | 按正则搜内容，带行号 |
| `todoWrite` | 记录多步任务清单 |
| `bash` | 跑 POSIX shell 命令（Windows 上是 Git Bash） |
| `pwsh` | 跑 PowerShell，用于注册表、服务、WMI、.NET |
| `open` | 用系统默认程序打开文件或文件夹 |
| `ping` | 连通性探测 |

完整参数说明见 [`docs/local-dev-mcp.md`](docs/local-dev-mcp.md)——这份文档会被自动上传到平台，模型读的就是它。

**路径**：所有相对路径都相对工作目录。工作目录里有一个 `Desktop/`，指向你真实的 Windows 桌面——所以"在我桌面做个页面"会真的落在桌面上，而不是某个模型自创的 `output/`。

---

## 它是怎么工作的

```
ScienceOne 云端  ──HTTPS──▶  *.trycloudflare.com  ──▶  cloudflared  ──▶  127.0.0.1:8801  ──▶  你的磁盘
        │                                                                      ▲
        └── 网页（装在 WebView2 窗口里）── 注入面板 ── 自动填 MCP 表单 ─────────┘
```

几个设计取舍：

- **不重造穿透轮子。** 隧道交给 cloudflared，它一个静态二进制、quick tunnel 不需要账号。
- **一个进程干完全部。** MCP 服务、隧道、窗口在同一个可执行文件里，没有需要单独启动的依赖服务。
- **首次运行零配置。** 令牌自动生成、工作目录自动推断、桌面自动链接、`python3`/`pip3` 自动加别名（Windows 上不存在这两个命令，但模型永远会这么写）。
- **面板是注入的，不是改平台。** 用 WebView2 的 `Init` 钩子在每次导航前注入脚本，平台自身的代码一行没动。

---

## 常见问题

**「更新失败 / 目标 URL 不可达」**

平台在保存 MCP 条目之前会真的去连一次你填的地址，连不上就直接拒绝保存。

quick tunnel 的地址在发下来之后还要等一段时间才真正可达——实测从 20 秒到十几分钟都有。所以面板上「公网隧道」下面还有一行「隧道可达」：

- **预热中…**：地址有了，但本机自己都还没打通。这时候先别点填入。
- **已自测通过**：地址确实能用，现在点 **一键填入ScienceOne表单**。

程序会自动等这一步（最多三分钟），正常情况不用你操心。

如果显示「已自测通过」之后平台**仍然**报不可达，那就是平台服务器到 `*.trycloudflare.com` 这一段的问题——国内到 Cloudflare 的线路并不总是稳。重试几次都不行的话，就换隧道（见下一条）。

**公网地址每次都变，要手动改吗？**

不用。面板每次启动都会找到已有的 `desktop` 条目并走**更新**路径，把新地址写回去。平台不允许同名重复创建，所以它必须先找到旧的。

**能固定地址吗？**

能，用 frp 对着你自己的服务器。服务端一行脚本、前端一层 TLS、客户端六个参数，全在 [deploy/frps.md](deploy/frps.md)。

quick tunnel 的设计就是不保证地址稳定——换来的是零账号、零配置。一旦你想把它当日常工具用，frp 那条路值得走：地址固定，也不再看 Cloudflare 的脸色。

**怎么知道该换隧道了？**

面板上「隧道可达」显示 `已自测通过`，但平台仍然报不可达——那就是平台到 Cloudflare 这一段不通。重试几次都不行就换 frp。

**模型说它看不到我的文件。**

先确认对话页的工具菜单里 `desktop` 是打开的；再确认面板里的工作目录就是你放代码的地方。平台侧调用失败时，`%LOCALAPPDATA%\scienceone-desktop\mcp.log` 会记下每一次请求。

**端口被占用。**

```
启动.cmd -port 8811
```

---

## 安全边界

- MCP 服务**只监听 `127.0.0.1`**，外网只能通过隧道进来。
- 公网端点要求 `X-MCP-Token` 正确，否则 401。令牌存在本机配置文件里，首次运行随机生成。
- 文件操作被限制在工作目录内，`../` 逃逸会被拒绝。唯一的例外是 `Desktop/`，而它也不能用来向外爬（`Desktop/..` 同样被拒）。
- 命令执行有超时上限（默认 180 秒，可调到 900 秒）。
- 工作目录里放什么，模型就能读什么。**不要把令牌、私钥、生产凭据放在工作目录里。**

---

## 从源码构建

```bat
git clone https://github.com/yg/scienceone-desktop
cd scienceone-desktop
go build -ldflags="-s -w" -o scienceone-desktop.exe .
```

纯 Go，不需要 CGO——窗口用的是 [go-webview2](https://github.com/jchv/go-webview2) 对 WebView2 的直接绑定。

端到端自检（需要一个正在运行的实例）：

```bat
node tests/smoke-test.mjs --token <token>
```

它用平台同款协议打一遍全部 12 个工具，覆盖握手、路径逃逸、超时、输出截断和补丁锚点。

---

## 已知限制

- 只支持 Windows（WebView2 + Git Bash）。
- 对话页的工具开关目前需要手动点一次，面板会给出提示。
- quick tunnel 的地址随进程变化，因此每次启动都会改写平台里的条目。
- **quick tunnel 的可达性不保证。** 实测平台侧到 `*.trycloudflare.com` 时通时不通（同一个地址在不同启动间从 6 秒到 11 分钟不等，有时始终不通），这是平台到 Cloudflare 的线路问题，本程序修不了。要稳定就按 [deploy/frps.md](deploy/frps.md) 换成自建 frp。
- **有些网络的本地 DNS 解析不了 `*.trycloudflare.com`。** 开发这台机器就是这样：本地解析器直接返回 NXDOMAIN（0 秒失败，不是超时），而 `223.5.5.5` 和 `8.8.8.8` 都正常。程序探测隧道时会自动回退到公共 DNS，所以面板上的「隧道可达」是可信的。
- **frp 这条路需要你有一台服务器**，而且 Windows Defender 会把 `frpc.exe` 判成 PUA 隔离，得手动加白名单。
