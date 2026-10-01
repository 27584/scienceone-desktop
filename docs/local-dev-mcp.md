# 本机开发环境 MCP（desktop）

为对话提供对开发者工作站的**受控访问**：浏览工程目录、读写源码、按片段打补丁、正则检索、执行构建与测试命令、查询 git 状态。

---

## 什么时候该用这些工具（重要）

磐石自身已经具备很强的能力——联网检索、读文献、生成报告与文档、产出下载文件。**这些场景优先用平台原生能力，不要绕到本机来。**

只在下面这类**必须动用户电脑**的情况下才调用本 MCP：

- 读写用户本机磁盘上的文件、目录
- 在用户的工程里检索代码、定位定义与引用
- 修改本机代码、打补丁、批量替换
- 在本机执行命令：编译、跑测试、装依赖、跑脚本
- 查看本机仓库的 git 状态与 diff
- 用本机默认程序打开文件或文件夹

反例（**不要**调用本 MCP）：单纯问答、资料检索、写一篇报告、生成一张图、把内容整理成文档并交给平台下载——这些用平台自己的工具完成即可，调用本机会平白增加一轮往返。

适用场景：代码阅读、缺陷定位、重构、补丁修改、单元测试编写、依赖与构建问题排查、脚本自动化。

---

## 连接方式

| 项 | 值 |
|---|---|
| 传输协议 | Streamable HTTP (MCP) |
| 端点 | `https://newapi.154.83.15.131.nip.io/panshi-mcp` |
| 鉴权 | 自定义请求头 `X-MCP-Token` |
| 协议版本 | 服务端回显客户端在 `initialize` 中提交的版本（支持 `2024-11-05` / `2025-03-26` / `2025-06-18`） |
| 工作目录 | `D:\Projects`（所有相对路径的基准，路径逃逸一律拒绝） |
| 桌面 | 用户的真实 Windows 桌面以 `Desktop/` 出现在工作目录内 |

### 运行环境（重要）

**这是一台 Windows 机器，不是 Linux。** shell 是 Git Bash（MSYS2 / MINGW64），不是 Linux 上的 bash。

- 没有 `xdg-open`、`sensible-browser`、`chromium`、`firefox`、`apt`、`apt-get`。
- 打开文件用 `open_path` 工具，**不要**在 shell 里试图启动查看器。
- 调用 Windows 程序要写全路径，例如 `/c/Windows/System32/cmd.exe`（`cmd.exe` 不在 Git Bash 的 PATH 里）。
- 路径用正斜杠：`D:/Projects/foo`。

每条 `run_shell` 结果都会带一行 `env=` 说明宿主环境；命令不存在时还会附上纠正提示。

---

## 工具列表

### `list_dir`

列出目录条目，目录名以 `/` 结尾。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 否 | 相对工作目录的路径，默认 `.` |
| `depth` | integer | 否 | 递归展开层数。`1` = 只列本层（默认），`2` = 含子目录，最大 `6` |

**摸清工程结构时直接给 `depth: 2` 或 `3`**，一次调用拿到整棵树，不要一层层翻。递归结果按缩进展开，条目超过 800 条会截断并提示收窄路径。

### `read_file`

读取 UTF-8 文本文件，返回**带 1 起始行号**的内容。大文件用 `start_line` / `end_line` 分段读取。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 相对工作目录的文件路径 |
| `start_line` | integer | 否 | 起始行（含） |
| `end_line` | integer | 否 | 结束行（含） |

文件太大时返回内容会被截断，回复末尾会告诉你从第几行继续读。

### `write_file`

写入完整 UTF-8 文件，父目录自动创建。**整文件覆盖**。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 相对工作目录的文件路径 |
| `content` | string | 是 | 写入的完整内容 |

### `apply_patch`

在已有文件中替换一段精确文本。**修改已有代码的首选方式。**

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 相对工作目录的文件路径 |
| `old_string` | string | 是 | 必须与文件内容**逐字节**一致（含缩进） |
| `new_string` | string | 是 | 替换后的文本 |
| `replace_all` | boolean | 否 | 是否替换全部匹配，默认 `false` |

`old_string` 出现多次时会报错，此时多带几行上下文以唯一定位，或设置 `replace_all: true`。

### `find_files`

按路径模式（glob）查找文件。知道文件名大概叫什么、但不知道在哪时用它。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `pattern` | string | 是 | glob 模式，`**` 匹配任意层目录，如 `**/*.py`、`src/**/*.ts` |
| `path` | string | 否 | 检索起点，默认工作目录 |
| `max_results` | integer | 否 | 返回上限，默认 200 |

### `search_code`

在工作目录下按正则检索文件内容，返回 `路径:行号: 内容`。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `pattern` | string | 是 | 正则表达式 |
| `path` | string | 否 | 检索起点，默认工作目录 |
| `glob` | string | 否 | 文件名过滤，如 `*.py` |
| `max_results` | integer | 否 | 返回条数上限，默认 80 |
| `case_sensitive` | boolean | 否 | 是否区分大小写，默认 `false`（找半记得的符号时正合适；需要精确匹配时设 `true`） |

只搜内容，不搜文件名——找文件名用 `find_files`。

### `replace_in_files`

在多个文件里批量替换**字面量**字符串（非正则），返回改动了哪些文件。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `old_string` | string | 是 | 要查找的精确文本 |
| `new_string` | string | 是 | 替换后的文本 |
| `path` | string | 否 | 检索起点，默认工作目录 |
| `glob` | string | 否 | 文件名过滤，如 `*.py` |
| `max_files` | integer | 否 | 改动文件数上限，默认 50 |

机械性改名用它；需要上下文才能唯一定位的编辑用 `apply_patch`。

### `run_shell`

执行一条 shell 命令，返回合并后的输出与退出码。

**shell 是 POSIX shell（Windows 上为 Git Bash）**，因此 `||`、`&&`、管道、`2>&1`、命令替换都可直接使用。路径请用**正斜杠**（`D:/Projects/foo`）或相对路径；反斜杠在 shell 中是转义符，虽会自动纠正，直接写正斜杠更可靠。`python3`、`pip3`、`node`、`npm`、`git` 等命令名已做映射，可正常调用。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `command` | string | 是 | 要执行的命令 |
| `cwd` | string | 否 | 相对工作目录的执行位置，默认工作目录 |
| `timeout` | integer | 否 | 允许执行的秒数，默认 180，最大 900 |

**装依赖、跑全量测试、构建这类任务要显式调大 `timeout`**，否则 180 秒一到就被终止，返回 `[timed out after Ns and was terminated]`。命令需要交互输入的话永远不会结束，改写成非交互形式（`-y`、`--yes`、`--batch`、重定向 `< /dev/null`）。

输出超过 3 万字符时，**保留开头与结尾，省略中间**——退出码和报错行都还在。想看清中间部分就重跑并加过滤，例如 `| tail -50`、`-q`、`--tb=short`。

### `git_status`

查看仓库当前分支、已暂存/未暂存/未跟踪的文件。只读。改动前后各看一次，便于如实汇报动了什么。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `cwd` | string | 否 | 仓库目录，默认工作目录 |

### `git_diff`

查看 diff，可限定单个文件。只读。用于提交前自查改动。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 否 | 只 diff 这个文件 |
| `cwd` | string | 否 | 仓库目录，默认工作目录 |
| `staged` | boolean | 否 | 看暂存区 diff（`--cached`） |

### `open_path`

用操作系统的默认程序打开文件或文件夹：`.html` 交给浏览器，文本交给关联编辑器，文件夹交给资源管理器。

**这是唯一的打开方式。** 不要在 `run_shell` 里试图启动查看器——这台机器没有 `xdg-open`。

| 参数 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `path` | string | 是 | 要打开的文件或文件夹，相对工作目录 |

### `ping`

连通性探测，返回工作目录、平台、桌面是否可达与 Node 版本。

---

## 推荐工作流程

1. **先探查，再动手。** 接到任务后先用 `list_dir` 带 `depth: 2` 一次看清工程结构，用 `search_code` 定位相关定义与引用，再用 `read_file` 读取原文。不要基于猜测直接覆盖文件。
2. **用户说"桌面"就写 `Desktop/`。** 不要自己发明 `output/` 之类的目录——用户找不到就等于没做。
3. **改已有代码用 `apply_patch`。** 先 `read_file` 拿到逐字节一致的原文，再构造 `old_string`。只有新建文件或整体重写才用 `write_file`。
4. **改完即验。** 每次修改后立刻用 `run_shell` 执行验证——编译、跑测试、语法检查，至少做一项，并如实报告退出码。
5. **多步推进，不要一步到位。** 一个任务通常需要多次工具调用：定位 → 读取 → 修改 → 验证 → 修正。不要在没有读取文件的情况下直接写补丁。
6. **长任务提前给足 `timeout`。** 装依赖、构建、全量测试默认 180 秒不够，按预期耗时显式传入，别等它被终止再重来。
7. **用户说"打开"就用 `open_path`。** 不要拼 shell 命令去猜怎么打开文件。
8. **报告要具体。** 说明改了哪个文件的哪个部分、为什么这样改、执行了什么验证命令、输出结果是什么。失败就贴原始错误，不要臆测成功。

---

## 典型调用序列

**阅读一个模块**

```
list_dir   { "path": "某工程目录", "depth": 2 }
search_code{ "pattern": "class .*Handler", "glob": "*.py" }
read_file  { "path": "某工程目录/handlers.py" }
```

**修一个 bug**

```
search_code{ "pattern": "def parse_config" }
read_file  { "path": ".../config.py" }
apply_patch{ "path": ".../config.py", "old_string": "<原文片段>", "new_string": "<修正后>" }
run_shell  { "command": "python3 -m pytest tests/test_config.py -q" }
```

**装依赖后跑测试**

```
run_shell  { "command": "python3 -m pip install -r requirements.txt", "timeout": 600 }
run_shell  { "command": "python3 -m pytest -q --tb=short", "timeout": 600 }
```

**新建并验证一个脚本**

```
write_file { "path": "scripts/check.py", "content": "<完整脚本>" }
run_shell  { "command": "python3 scripts/check.py" }
```

**在桌面做一个页面并打开**（"在我桌面做个 xxx" 就走这条）

```
write_file { "path": "Desktop/xxx.html", "content": "<完整 HTML>" }
open_path  { "path": "Desktop/xxx.html" }
```

---

## 安全边界

- 文件操作被限制在工作目录 `D:\Projects` 内，`../` 逃逸会被拒绝并返回错误。
- 唯一例外是 `Desktop/`，它指向用户真实桌面；这个链接不能用来向外爬（`Desktop/..` 同样会被拒绝）。
- 命令执行有超时上限（默认 180 秒，可按次调大至 900 秒），超时会被终止并明确标注。
- 端点仅接受持有正确 `X-MCP-Token` 的请求，其余返回 401。
- 该服务仅供账号持有人在本机开发环境中使用，不对第三方提供访问。

---

## 错误处理

工具失败时返回结构化错误文本，可直接依据内容调整参数后重试：

- `path escapes workspace root: ...` — 路径越界，改用工作目录内的相对路径。
- `no such directory: ...` — 路径不存在；回复里会附上工作目录顶层清单，据此重试。
- `old_string not found. Read the file first and copy the exact text, including indentation.` — 补丁锚点不匹配，重新 `read_file` 取原文。
- `old_string appears N times.` — 锚点重复，补充上下文或设置 `replace_all=true`。
- `old_string and new_string are identical` — 补丁没有实际改动，检查是否拿错了片段。
- `no such file or directory: ...` — 文件不存在，先用 `list_dir` 或 `find_files` 确认。
- `[timed out after Ns and was terminated]` — 命令超时，调大 `timeout` 重跑；若它本就需要交互输入，改写成非交互形式。
- `[... N characters omitted from the middle ...]` — 输出过长，已保留开头与结尾；要看中间就加过滤重跑。
- `command not found` / `exit=127` — 命令在这台 Windows 机器上不存在（常见于 `xdg-open`、`apt`）。打开文件改用 `open_path`。
- `exit=<非零>` — 命令本身失败，输出即真实报错，按报错修正后重跑。
