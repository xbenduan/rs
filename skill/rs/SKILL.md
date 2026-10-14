---
name: rs
description: Use the rs remote administration tool to install or safely upgrade rs, claim or import nodes, reset server credentials, run remote commands, transfer files, and check health.
user-invocable: true
---

# rs - Remote Shell & Upload

`rs` 是单二进制远程运维工具。服务端通过“账号 URL + 密码”签发短期会话，
客户端使用该会话执行 gRPC shell、上传与健康检查。公网统一使用 HTTPS `443`；
推荐由 Caddy 终止 TLS，`rs serve` 只监听 `127.0.0.1:8999`。

## 一、先安装客户端，再按节点状态决定是否部署服务端

始终使用 GitHub latest Release，产物根地址固定为：

```text
https://github.com/xbenduan/rs/releases/latest/download
```

首先完成客户端安装：

1. 从该地址下载 `SHA256SUMS`。
2. 根据客户端操作系统和架构下载 `rs-{darwin|linux}-{amd64|arm64}` 或
   `rs-windows-{amd64|arm64}.exe`。
3. 严格对照 `SHA256SUMS` 校验后，将客户端安装为 `~/.local/bin/rs`；Windows 安装到
   用户 PATH 中的 `rs.exe`。
4. 执行 `node list --json`。已有节点时不重复部署服务端；没有节点时再按第三节分流。

只有用户提供全新服务器 SSH 连接时才部署服务端：检查远端架构，安装并校验 Linux
二进制，配置认证状态、systemd 与 HTTPS，领取节点后验证 `health`。首次接入必须通过
`rs shell` 返回主机名、内核和 uptime，向用户证明命令真实来自 VPS。

禁止从 Git 仓库寻找二进制、从源码临时构建或跳过校验。缺少节点凭证且缺少 SSH、域名
或目标 VPS 信息时，明确请求这些输入。完整命令见 [install.md](references/install.md)。

## 二、定位正确的 rs

macOS 自带 `/usr/bin/rs`，它不是本 Skill 所需工具。依次检查
`~/.local/bin/rs` 和 `command -v rs`，且必须验证输出包含
`remote shell and file upload over gRPC`。不得把 Skill 中的历史版本号当作安装版本：

```bash
if [ -x ~/.local/bin/rs ] &&
  ~/.local/bin/rs 2>&1 | grep -q "remote shell and file upload"; then
  RS_BIN=~/.local/bin/rs
elif command -v rs >/dev/null 2>&1 &&
  rs 2>&1 | grep -q "remote shell and file upload"; then
  RS_BIN=$(command -v rs)
else
  echo "install rs from the GitHub latest Release" >&2
  exit 1
fi
```

## 三、首次使用状态机

每次任务首先执行：

```bash
"$RS_BIN" node list --json
```

### 已有节点

用户提供了节点 JSON 时，先按“登记或更新节点”直接更新，再使用指定节点。否则使用用户
指定节点；未指定且仅有一个节点时使用该节点。执行任何任务前先运行：

```bash
"$RS_BIN" health <node>
```

Health 也需要完整账号密码登录，因此成功即证明 URL、密码、TLS 与 gRPC 均有效。

### 没有节点

用户已经提供节点 JSON 时，直接按“登记或更新节点”执行，不再索要 SSH。没有节点 JSON
时不得猜测服务器或继续执行。获得一次性领取链接后，Agent 不得代为执行，而是立即要求
用户在自己的本地终端运行：

```bash
~/.local/bin/rs claim 'https://rs.example.com/claim/<one-time-token>'
```

Agent 必须等待用户明确确认领取成功，再执行 `node list --json` 和后续 Health。Agent
命令工具可能受工作区沙箱限制，无法写入用户的 `~/.rs/node.json`；不得改用项目内临时
节点文件规避该边界。链接消费一次后失效，但消费前仍不得写入文件或日志。没有领取链接
时，要求用户提供以下任一种输入：

```text
[
  {
    "name": "prod",
    "url": "https://rs.example.com/r/<account-id>",
    "password": "<password>"
  }
]
```

或者用于首次自动部署的 SSH 连接：

```text
ssh;host=1.2.3.4;user=root;port=22;password=<temporary-password>;domain=rs.example.com
ssh;host=1.2.3.4;user=root;port=22;key=~/.ssh/id_ed25519;domain=rs.example.com
```

字段名固定使用 `host` 和 `port`。SSH 密码仅用于本次部署：不得回显、写入文件、
写入日志或拼进可被进程列表读取的命令。私钥字段必须是本地路径，不要求用户粘贴
私钥内容。

## 四、登记或更新节点

收到一个或多个完整节点信息时，优先使用 JSON 数组批量更新：

```bash
"$RS_BIN" node add --json
```

启动命令后通过 stdin 直接传入 JSON，不得把 JSON 拼进命令参数、环境变量、临时文件或
日志。命令会严格校验全部节点，逐个完成真实 HTTPS 登录，记录连接的真实对端 IP，并且
仅在全部成功后原子更新 `~/.rs/node.json`；同名或同稳定 ID 节点覆盖，未涉及节点保留。

仅登记单个节点且密码可通过隐藏终端或 stdin 单独提供时，也可以使用：

```bash
printf '%s' '<password>' |
  "$RS_BIN" node add <name> \
  'https://rs.example.com/r/<account-id>'
```

`node add` 会先完成真实 HTTPS 登录，验证失败时不会保存节点。节点保存在
`~/.rs/node.json`；macOS/Linux 使用 `0700` 目录与 `0600` 文件，Windows 使用当前用户
DPAPI 加密密码。服务端返回的稳定 ID 是节点身份；真实连接 IP 只作观察记录，不参与
合并。

## 五、通过 SSH 自动部署

部署前确认：

1. 域名 A/AAAA 记录已经指向服务器。
2. 安全组和主机防火墙允许 TCP `80/443`。
3. 远端架构是 `linux/amd64` 或 `linux/arm64`。
4. 客户端和服务端必须使用同一个 GitHub Release 中经过校验的二进制。

部署目标：

```text
Internet :443 -> Caddy HTTPS -> h2c://127.0.0.1:8999 -> rs
```

在远端初始化一次：

```bash
/opt/rs/rs init \
  --name prod \
  --endpoint https://rs.example.com \
  --state /etc/rs/auth.json \
  --claim-url-file /root/prod.rs-claim-url
```

初始化不输出密码，只报告领取链接文件路径。安装 systemd 服务：

```ini
[Unit]
Description=rs remote shell
After=network-online.target
Wants=network-online.target

[Service]
User=root
Group=root
ExecStart=/opt/rs/rs serve --listen 127.0.0.1:8999 --state /etc/rs/auth.json
Restart=on-failure
RestartSec=3
UMask=0077
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Caddy 配置：

```caddyfile
rs.example.com {
  reverse_proxy h2c://127.0.0.1:8999
}
```

不要为该站点启用包含完整请求路径的 access log。启动并验证 Caddy 和 rs 后，Agent
读取 `/root/prod.rs-claim-url`，把完整的 `~/.local/bin/rs claim '<url>'` 命令发给用户，
要求用户在本地终端执行。用户明确确认成功后，Agent 才能删除远端链接文件，并运行
`node list --json`、`health` 与：

```bash
"$RS_BIN" shell <node> 'hostname; uname -srmo; uptime'
```

必须把这条 Shell 的输出反馈给用户，作为首次安装真实连通 VPS 的证明。

## 六、安全升级

用户要求升级客户端、服务端或“部署最新版本”时，必须先完整读取
[upgrade.md](references/upgrade.md) 并严格执行。升级必须满足：

1. 从 GitHub latest Release 下载 `SHA256SUMS` 和目标平台产物并校验。
2. 升级前确认当前 Health 成功，并保留可用 SSH 回滚通道。
3. 新服务先以独立 unit、独立 loopback 端口和认证状态副本启动。
4. Caddy 切到候选服务后，使用新客户端完成 HTTPS Health 与 Shell 验证。
5. 正式服务使用新二进制重启并再次完成公网 Health 后，才允许删除候选服务和旧二进制。
6. 任一步失败立即恢复 Caddy 与旧服务，不得继续清理。

禁止直接覆盖运行中的 `/opt/rs/rs` 后重启，也禁止先卸载旧服务再部署新服务。

## 七、常用操作

```bash
"$RS_BIN" shell <node> 'echo hello && cd /tmp && ls -la'
"$RS_BIN" push <node> ./local.txt /tmp/remote.txt --mkdir --timeout 300
"$RS_BIN" health <node>
"$RS_BIN" node list --json
"$RS_BIN" node add --json
"$RS_BIN" node rm <name>
```

- shell 脚本整段作为一个参数传入，服务端通过 `sh -c` 原样执行。
- 默认超时 30 秒，超时杀死整个进程组；远端退出码原样透传。
- push 目标必须是绝对路径；默认原子拒绝覆盖，`--overwrite` 才允许覆盖；完整调用默认
  300 秒超时，可通过 `--timeout N` 调整。
- 所有远程能力都要求 HTTPS 密码登录后签发的短期会话。

## 八、在 VPS 本机重置凭证

rs 不提供公网恢复接口，也不生成离线恢复凭证。需要重置时，必须持有独立 SSH 通道，
停止服务后在 VPS 本机直接操作认证 state：

```bash
systemctl stop rs
rm -f /root/prod.rs-claim-url
/opt/rs/rs credential reset \
  --state /etc/rs/auth.json \
  --claim-url-file /root/prod.rs-claim-url
systemctl start rs
```

该命令会重建 account ID、密码、session key 和单次领取密文，旧账号、密码及 session
全部失效。服务启动后，Agent 读取新链接并要求用户在本地终端执行 `rs claim`；用户确认
成功后，再运行 `health` 和首次 Shell 探测。该命令保留节点 ID；服务未停止时会因 state
被锁定而失败。

## 九、安全边界

- 账号 URL 是用户名而非密码，但不应长期写入公开日志。
- Agent 获取一次性链接后只向用户提供本地 `rs claim` 命令，不得代为执行。
- 领取响应仅含密文；链接在消费前仍等同临时最高权限凭证。
- 节点 JSON 包含明文密码，只能经 stdin 传入，不得放入命令参数、环境变量或日志。
- 生产公网只允许 HTTPS；HTTP 仅允许 loopback 测试。
- 不得使用 `--insecure`、跳过证书校验或自签证书规避 TLS。
- 服务端认证状态与客户端节点文件均包含敏感材料；macOS/Linux 文件权限必须为 `0600`，
  Windows 节点密码必须保持 DPAPI 加密。
- 不得将密码放入 URL、查询参数、命令行位置参数或 shell 历史。
- 失败时先区分 DNS、TLS、HTTP 登录、gRPC 会话和远端命令退出码，不得盲目重试。

## References

- [api.md](references/api.md)：命令、认证协议与错误语义
- [install.md](references/install.md)：安装、Caddy、systemd 与首次部署
- [upgrade.md](references/upgrade.md)：候选服务验证、正式切换与回滚流程
