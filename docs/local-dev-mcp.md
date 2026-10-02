# 本机开发环境 MCP（desktop）

为对话提供对开发者工作站的**受控访问**：浏览工程目录、读写源码、按片段打补丁、正则检索、执行构建与测试命令、查看 git 状态。

工具名与参数沿用主流编码工具链的约定（`read` / `write` / `edit` / `glob` / `grep` / `bash`），无需额外记忆。

---

## 什么时候该用这些工具（重要）

ScienceOne（磐石）自身已经具备很强的能力——联网检索、读文献、生成报告与文档、产出下载文件。**这些场景优先用平台原生能力，不要绕到本机来。**

只在下面这类**必须动用户电脑**的情况下才调用本 MCP：

- 读写用户本机磁盘上的文件、目录
- 在用户的工程里检索代码、定位定义与引用
- 修改本机代码、打补丁、批量替换
- 在本机执行命令：编译、跑测试、装依赖、跑脚本
- 用本机默认程序打开文件或文件夹

反例（**不要**调用本 MCP）：单纯问答、资料检索、写一篇报告、生成一张图、把内容整理成文档并交给平台下载——这些用平台自己的工具完成即可，调用本机会平白增加一轮往返。

适用场景：代码阅读、缺陷定位、重构、补丁修改、单元测试编写、依赖与构建问题排查、脚本自动化。

---

## 连接方式

| 项 | 值 |
|---|---|
| 传输协议 | Streamable HTTP (MCP) |
| 端点 | 本条目中填写的 URL |
| 鉴权 | 自定义请求头 `X-MCP-Token` |
| 协议版本 | 服务端回显客户端在 `initialize` 中提交的版本 |
| 工作目录 | 所有相对路径的基准；路径逃逸一律拒绝 |
| 桌面 | 用户的真实 Windows 桌面以 `Desktop/` 出现在工作目录内 |

### 运行环境（重要）

**这是一台 Windows 机器，不是 Linux。** `bash` 走的是 Git Bash（MSYS2 / MINGW64），不是 Linux 上的 bash。

- 没有 `xdg-open`、`sensible-browser`、`chromium`、`firefox`、`apt`、`apt-get`。
- 打开文件用 `open` 工具，**不要**在 shell 里试图启动查看器。
- 调用 Windows 程序要写全路径，例如 `/c/Windows/System32/cmd.exe`（`cmd.exe` 不在 Git Bash 的 PATH 里）。
- 路径用正斜杠：`D:/Projects/foo`。
- 需要动注册表、服务、WMI、.NET 类型时改用 `pwsh`，不要硬塞进 `bash`。

`bash` / `pwsh` 的结果末尾会带一行 `[exit code: N]`；**非零退出不代表工具调用失败**，输出里就是真实报错，按报错修正后重跑。

---

## 工具列表

### `ls`

列出目录条目，目录名以 `/` 结尾。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 否 | 相对工作目录的路径，默认工作目录根 |
| `depth` | integer | 否 | 递归展开层数。`1` = 只列本层（默认），`2` = 含子目录，最大 `6` |

**摸清工程结构时直接给 `depth: 2` 或 `3`**，一次调用拿到整棵树，不要一层层翻。递归结果按缩进展开，条目过多会截断并提示收窄路径。

### `read`

读取 UTF-8 文本文件，返回**带行号**的内容。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 相对工作目录的文件路径 |
| `offset` | integer | 否 | 起始行，1 起始，默认 `1` |
| `limit` | integer | 否 | 读取行数，默认读到文件末尾 |

**改文件前必须先 `read`。** 大文件用 `offset` + `limit` 分段读。内容过长时结果会保留开头与结尾，并在末尾告诉你从哪里继续。

### `readLines`

与 `read` 同参数，但返回**结构化行数组**（含精确行号的 JSON），适合需要把行号喂给 `edit` 的场景。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 相对工作目录的文件路径 |
| `offset` | integer | 否 | 起始行，1 起始 |
| `limit` | integer | 否 | 读取行数 |

### `write`

写入完整 UTF-8 文件，父目录自动创建。**整文件覆盖。**

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 相对工作目录的文件路径 |
| `content` | string | 是 | 写入的完整内容 |

已有文件优先用 `edit`，只有新建或整体重写才用 `write`。

### `edit`

在已有文件中替换一段精确文本。**修改已有代码的首选方式。**

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 相对工作目录的文件路径 |
| `oldString` | string | 是 | 必须与文件内容**逐字节**一致（含缩进） |
| `newString` | string | 是 | 替换后的文本 |
| `replaceAll` | boolean | 否 | 是否替换全部匹配，默认 `false` |

`oldString` 出现多次时会报错，此时多带几行上下文以唯一定位，或设置 `replaceAll: true`。

### `glob`

按路径模式查找文件。知道文件名大概叫什么、但不知道在哪时用它。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `pattern` | string | 是 | glob 模式，`**` 匹配任意层目录，如 `**/*.py`、`src/**/*.ts` |
| `path` | string | 否 | 检索起点，默认工作目录根 |
| `maxResults` | integer | 否 | 返回上限，默认 `200` |

只返回路径，不返回内容。

### `grep`

在工作目录下按正则检索文件内容，返回 `路径:行号: 内容`。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `pattern` | string | 是 | 正则表达式 |
| `path` | string | 否 | 检索起点，默认工作目录根 |
| `glob` | string | 否 | 文件名过滤，如 `*.py` |
| `maxResults` | integer | 否 | 返回条数上限，默认 `80` |
| `caseSensitive` | boolean | 否 | 是否区分大小写，默认 `false`（找半记得的符号时正合适；需要精确匹配时设 `true`） |

只搜内容，不搜文件名——找文件名用 `glob`。

### `todoWrite`

记录或更新当前工作的结构化任务列表。**每次发送完整列表，整体替换上一版**（没有增量更新）。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `todos` | array | 是 | 完整任务列表 |
| `todos[].content` | string | 是 | 简短祈使句描述 |
| `todos[].status` | string | 是 | `pending` \| `in_progress` \| `completed` |

用于规划多步工作并向用户同步进度。同一时刻只保留一项 `in_progress`。

### `bash`

执行一条 POSIX shell 命令（Windows 上为 Git Bash），返回合并后的输出与退出码。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `command` | string | 是 | 要执行的命令 |
| `cwd` | string | 否 | 相对工作目录的执行位置，默认工作目录 |
| `timeout` | integer | 否 | 允许执行的秒数，默认 `180`，最大 `900` |

`||`、`&&`、管道、`2>&1`、命令替换都可直接使用。路径请用**正斜杠**（`D:/Projects/foo`）或相对路径；反斜杠在 shell 中是转义符，虽会自动纠正，直接写正斜杠更可靠。`python3`、`pip3`、`node`、`npm`、`git` 等命令名已做映射，可正常调用。

**装依赖、跑全量测试、构建这类任务要显式调大 `timeout`**，否则 180 秒一到就被终止，返回 `[timed out after Ns and was terminated]`。命令需要交互输入的话永远不会结束，改写成非交互形式（`-y`、`--yes`、`--batch`、重定向 `< /dev/null`）。

输出超过 3 万字符时，**保留开头与结尾，省略中间**——退出码和报错行都还在。想看清中间部分就重跑并加过滤，例如 `| tail -50`、`-q`、`--tb=short`。

### `pwsh`

执行一条 PowerShell 命令，返回输出与退出码。仅在 `bash` 别扭时使用：Windows API、注册表、服务、WMI、.NET 类型。日常文件与构建工作请用 `bash`。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `command` | string | 是 | PowerShell 命令 |
| `cwd` | string | 否 | 相对工作目录的执行位置 |
| `timeout` | integer | 否 | 允许执行的秒数，默认 `180`，最大 `900` |

### `open`

用操作系统的默认程序打开文件或文件夹：`.html` 交给浏览器，文本交给关联编辑器，文件夹交给资源管理器。

**这是唯一的打开方式。** 不要在 `bash` 里试图启动查看器——这台机器没有 `xdg-open`。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 要打开的文件或文件夹，相对工作目录 |

### `ping`

连通性探测，返回工作目录、平台、桌面是否可达。无参数。

---

## 推荐工作流程

1. **先探查，再动手。** 接到任务后先用 `ls` 带 `depth: 2` 一次看清工程结构，用 `grep` 定位相关定义与引用，再用 `read` 读取原文。不要基于猜测直接覆盖文件。
2. **用户说"桌面"就写 `Desktop/`。** 不要自己发明 `output/` 之类的目录——用户找不到就等于没做。
3. **改已有代码用 `edit`。** 先 `read` 拿到逐字节一致的原文，再构造 `oldString`。只有新建文件或整体重写才用 `write`。
4. **改完即验。** 每次修改后立刻用 `bash` 执行验证——编译、跑测试、语法检查，至少做一项，并如实报告退出码。
5. **多步推进，不要一步到位。** 一个任务通常需要多次工具调用：定位 → 读取 → 修改 → 验证 → 修正。不要在没有读取文件的情况下直接写补丁。多步任务用 `todoWrite` 把计划摆出来。
6. **长任务提前给足 `timeout`。** 装依赖、构建、全量测试默认 180 秒不够，按预期耗时显式传入，别等它被终止再重来。
7. **用户说"打开"就用 `open`。** 不要拼 shell 命令去猜怎么打开文件。
8. **报告要具体。** 说明改了哪个文件的哪个部分、为什么这样改、执行了什么验证命令、输出结果是什么。失败就贴原始错误，不要臆测成功。

---

## 典型调用序列

**阅读一个模块**

```
ls    { "path": "某工程目录", "depth": 2 }
grep  { "pattern": "class .*Handler", "glob": "*.py" }
read  { "path": "某工程目录/handlers.py" }
```

**修一个 bug**

```
grep  { "pattern": "def parse_config" }
read  { "path": ".../config.py" }
edit  { "path": ".../config.py", "oldString": "<原文片段>", "newString": "<修正后>" }
bash  { "command": "python3 -m pytest tests/test_config.py -q" }
```

**装依赖后跑测试**

```
bash  { "command": "python3 -m pip install -r requirements.txt", "timeout": 600 }
bash  { "command": "python3 -m pytest -q --tb=short", "timeout": 600 }
```

**新建并验证一个脚本**

```
write { "path": "scripts/check.py", "content": "<完整脚本>" }
bash  { "command": "python3 scripts/check.py" }
```

**在桌面做一个页面并打开**（"在我桌面做个 xxx" 就走这条）

```
write { "path": "Desktop/xxx.html", "content": "<完整 HTML>" }
open  { "path": "Desktop/xxx.html" }
```

---

## 安全边界

- 文件操作被限制在配置的工作目录内，`../` 逃逸会被拒绝并返回错误。
- 唯一例外是 `Desktop/`，它指向用户真实桌面；这个链接不能用来向外爬（`Desktop/..` 同样会被拒绝）。
- 命令执行有超时上限（默认 180 秒，可按次调大至 900 秒），超时会被终止并明确标注。
- 端点仅接受持有正确 `X-MCP-Token` 的请求，其余返回 401。
- 该服务仅供账号持有人在本机开发环境中使用，不对第三方提供访问。

---

## 错误处理

工具失败时返回结构化错误文本，可直接依据内容调整参数后重试：

- `path escapes workspace root: ...` — 路径越界，改用工作目录内的相对路径。
- `no such directory: ...` — 路径不存在；回复里会附上工作目录顶层清单，据此重试。
- `oldString not found. Read the file first and copy the exact text, including indentation.` — 补丁锚点不匹配，重新 `read` 取原文。
- `oldString appears N times.` — 锚点重复，补充上下文或设置 `replaceAll: true`。
- `oldString and newString are identical` — 补丁没有实际改动，检查是否拿错了片段。
- `no such file or directory: ...` — 文件不存在，先用 `ls` 或 `glob` 确认。
- `[timed out after Ns and was terminated]` — 命令超时，调大 `timeout` 重跑；若它本就需要交互输入，改写成非交互形式。
- `[... N characters omitted from the middle ...]` — 输出过长，已保留开头与结尾；要看中间就加过滤重跑。
- `command not found` / `[exit code: 127]` — 命令在这台 Windows 机器上不存在（常见于 `xdg-open`、`apt`）。打开文件改用 `open`。
- `[exit code: <非零>]` — 命令本身失败，输出即真实报错，按报错修正后重跑。
