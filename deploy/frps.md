# 用 frp 换一条稳定的隧道

## 什么时候需要这个

默认的 cloudflared quick tunnel 不需要任何准备，但它给出的是 `*.trycloudflare.com` 上的随机地址，而这个地址**平台不一定连得上**。

实测同一台机器：某次启动 6 秒就能从平台侧连通，另一次 11 分钟后仍然不行，`mcp.log` 里平台的探测请求一条没有 —— 那是平台服务器到 Cloudflare 的线路问题，本程序修不了。

如果你有一台能被平台访问到的服务器（你之前用的那台国内 VPS 就是），换成 frp 就能拿到一个**固定、随时可达**的地址，代价是一次性配置。

---

## 服务端（你的 VPS，一次性）

### 自动安装

```bash
scp deploy/frps-install.sh root@your-server:/root/
ssh root@your-server
chmod +x frps-install.sh
./frps-install.sh <token> <remotePort>
```

`<token>` 自己随便生成一串（`openssl rand -hex 16`），`<remotePort>` 是这台服务器上用来承载隧道的端口，比如 `18802`。

脚本会下载 frps、把配置写到 `/etc/frp/frps.toml`、装成 systemd 服务，并用 `allowPorts` **只放行你指定的那一个端口** —— 万一 token 泄漏，也变不成通用端口转发器。

### 手动安装

```bash
curl -fsSL -o frp.tar.gz https://github.com/fatedier/frp/releases/download/v0.71.0/frp_0.71.0_linux_amd64.tar.gz
tar xzf frp.tar.gz
install -m 0755 frp_0.71.0_linux_amd64/frps /usr/local/bin/frps
mkdir -p /etc/frp
```

`/etc/frp/frps.toml`：

```toml
bindPort = 7000
auth.token = "换成你自己的"
allowPorts = [{ start = 18802, end = 18802 }]
```

```bash
frps -c /etc/frp/frps.toml
```

### 防火墙

放行 `7000`（frp 控制连接）和 `18802`（隧道端口）：

```bash
ufw allow 7000/tcp && ufw allow 18802/tcp
```

---

## 一层 TLS（必须）

**平台只接受 HTTPS 地址。** frp 搬的是裸 TCP，证书得你自己这边终结。

### Caddy（最省事，自动申请证书）

```
mcp.example.com {
    reverse_proxy 127.0.0.1:18802
}
```

公网地址就是 `https://mcp.example.com`。

### nginx

```nginx
location /scienceone-mcp/ {
    proxy_pass http://127.0.0.1:18802/;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;

    # MCP 是请求/响应流，缓冲会让首字节一直等到结束
    proxy_buffering off;

    # 一次工具调用可能跑好几分钟（跑测试、装依赖）
    proxy_read_timeout 900s;
    proxy_send_timeout 900s;
}
```

公网地址是 `https://你的域名/scienceone-mcp`。

> 注意结尾的斜杠：`location /scienceone-mcp/` 配 `proxy_pass http://127.0.0.1:18802/` 会把前缀剥掉，所以平台看到的 `/mcp` 正好落在本机的 `/mcp` 上。少写一个斜杠就会变成 `/scienceone-mcp/mcp`，直连 404。

### 没有域名怎么办

用 `nip.io` 这类通配 DNS：`https://<你的IP>.nip.io/scienceone-mcp`。你之前那条 `newapi.154.83.15.131.nip.io` 就是这个用法。证书要能覆盖这个域名 —— Caddy 会用 HTTP-01 自动签。

---

## 客户端

```bat
scienceone-desktop.exe ^
  -tunnel frp ^
  -frp-server your-server ^
  -frp-port 7000 ^
  -frp-token <上面那个 token> ^
  -frp-remote 18802 ^
  -public-url https://你的域名/scienceone-mcp
```

这些值会写进 `%LOCALAPPDATA%\scienceone-desktop\config.json`，下次直接启动就行。

### frpc 二进制要自己放

程序按顺序找：

1. `PATH` 里的 `frpc`
2. 程序目录下的 `frpc.exe`
3. `%LOCALAPPDATA%\scienceone-desktop\bin\frpc.exe`

从 [fatedier/frp releases](https://github.com/fatedier/frp/releases) 下载 `frp_<版本>_windows_amd64.zip`，把里面的 `frpc.exe` 放到上面任一位置。

> **杀软会拦它。** frp 因为常被滥用，Windows Defender 经常把它判成 PUA 直接隔离（开发这台机器上 `frps.exe` 就是被这么删掉的）。需要手动加白名单，或换个目录再试。

---

## 验证

1. 启动后看面板：**隧道后端** 应显示 `frp · 自建服务器`，**公网隧道** 显示你的 `https://` 地址。
2. **隧道可达** 变成 `已自测通过` —— 这一步是本机经隧道绕一圈回来的实测。
3. 从**别的网络**（手机热点）访问 `https://你的域名/scienceone-mcp`，带请求头 `X-MCP-Token: <令牌>`，应该返回一段 JSON 列出 12 个工具。
4. 再点面板上的 **一键填入ScienceOne表单**。

---

## 排查

**面板显示"未连接"**：frpc 没起来。看 `%LOCALAPPDATA%\scienceone-desktop\` 下的 `frpc.toml` 和程序日志；`frpc -c frpc.toml` 可以单独手跑看报错。

**`start proxy success` 一直不出现**：服务端没放行 `remotePort`（`allowPorts`），或者防火墙挡了 `7000`。

**本机 curl 通、平台仍报不可达**：TLS 那一层没配对。用手机热点（换一条网络）验证，排除本机 DNS 的干扰。

**平台侧报 `目标 URL 不可达`**：核对 `-public-url` 和 nginx 的 `location` 前缀是否一致，尤其是结尾的斜杠。
