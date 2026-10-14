# rs 安装与部署

本文始终对应 GitHub latest Release。

## 发布产物

```text
rs-darwin-amd64
rs-darwin-arm64
rs-linux-amd64
rs-linux-arm64
rs-windows-amd64.exe
rs-windows-arm64.exe
skill.zip
SHA256SUMS
```

客户端与服务端必须使用同一个 latest Release。不得从 Git 仓库读取二进制，也不得
跳过校验：

```text
https://github.com/xbenduan/rs/releases/latest/download
```

## 安装客户端

macOS 或 Linux：

```bash
set -eu
base=https://github.com/xbenduan/rs/releases/latest/download
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac
asset="rs-$os-$arch"
tmp=$(mktemp -d)
curl -fL "$base/$asset" -o "$tmp/$asset"
curl -fL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
expected=$(awk -v file="$asset" '$2 == file {print $1}' "$tmp/SHA256SUMS")
actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
test -n "$expected" && test "$actual" = "$expected"
install -d "$HOME/.local/bin"
install -m 755 "$tmp/$asset" "$HOME/.local/bin/rs"
"$HOME/.local/bin/rs" 2>&1 | head -n 1
rm -rf "$tmp"
```

macOS 客户端应安装到 `~/.local/bin/rs`，避免命中系统自带 `/usr/bin/rs`。

Windows 使用 PowerShell，根据 `$env:PROCESSOR_ARCHITECTURE` 选择 `amd64` 或
`arm64`，下载对应 `.exe` 和 `SHA256SUMS`，用 `Get-FileHash -Algorithm SHA256`
比对后将文件保存为用户 PATH 中的 `rs.exe`。不得在校验前执行。

已安装的 macOS 或 Linux 客户端可直接更新：

```bash
~/.local/bin/rs update
```

命令只在 latest 版本更高且 SHA-256 校验通过后原子替换当前二进制。Windows 客户端仍按
上面的 PowerShell 流程手工更新。

## 安装 Linux 服务端

通过用户提供的 SSH 连接执行以下等价步骤：

```bash
set -eu
base=https://github.com/xbenduan/rs/releases/latest/download
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac
asset="rs-linux-$arch"
tmp=$(mktemp -d)
curl -fL "$base/$asset" -o "$tmp/$asset"
curl -fL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
expected=$(awk -v file="$asset" '$2 == file {print $1}' "$tmp/SHA256SUMS")
actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
test -n "$expected" && test "$actual" = "$expected"
install -d -m 755 /opt/rs
install -m 755 "$tmp/$asset" /opt/rs/rs
/opt/rs/rs 2>&1 | head -n 1
rm -rf "$tmp"
```

## 服务端初始化

确保域名已经解析到服务器，再执行一次初始化：

```bash
install -d -m 700 /etc/rs
/opt/rs/rs init \
  --name prod \
  --endpoint https://rs.example.com \
  --state /etc/rs/auth.json \
  --claim-url-file /root/prod.rs-claim-url
chmod 600 /etc/rs/auth.json /root/prod.rs-claim-url
```

终端输出只包含领取链接文件路径，不包含账号密码。节点凭证已经由二进制加密存入 state。

## systemd

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

```bash
systemctl daemon-reload
systemctl enable --now rs
systemctl status rs --no-pager
```

## Caddy

公网仅开放 TCP `80/443`，rs 后端端口不得对公网开放：

```caddyfile
rs.example.com {
  reverse_proxy h2c://127.0.0.1:8999
}
```

不得启用记录完整请求路径的 access log，否则一次性领取链接会泄露。

```bash
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

## 客户端登记

Agent 读取链接后，把领取命令交给用户。以下 `claim` 必须由用户在自己的本地终端执行：

```bash
~/.local/bin/rs claim 'https://rs.example.com/claim/<one-time-token>'
```

用户明确确认领取成功后，Agent 再执行：

```bash
ssh root@server 'rm -f /root/prod.rs-claim-url'
~/.local/bin/rs node list --json
~/.local/bin/rs health prod
~/.local/bin/rs shell prod 'hostname; uname -srmo; uptime'
```

服务端在返回密文信封前先原子销毁待领取状态，因此链接成功使用一次后立即失效。客户端
二进制使用链接 token 解密节点，将认证连接的真实对端 IP 一并写入 `~/.rs/node.json`。
Agent 命令沙箱可能不能写 `~/.rs`，因此不得代替用户执行 claim。Agent 必须把 Shell
输出反馈给用户，证明首次安装真实连接到了目标服务器。

已有账号 URL 与密码时，也可手动登记：

```bash
printf '%s' '<password>' |
  ~/.local/bin/rs node add prod \
  'https://rs.example.com/r/<account-id>'

~/.local/bin/rs health prod
~/.local/bin/rs shell prod 'hostname; uname -srmo; uptime'
```

`node add` 会先在线验证凭证。节点文件位于 `~/.rs/node.json`，目录权限 `0700`，
文件权限 `0600`。Windows 客户端使用当前用户 DPAPI 加密节点文件中的密码。

已有完整节点 JSON 数组时，直接通过 stdin 批量更新：

```bash
~/.local/bin/rs node add --json <<'JSON'
[
  {
    "name": "prod",
    "url": "https://rs.example.com/r/<account-id>",
    "password": "<password>"
  }
]
JSON
```

全部节点在线验证成功后才会原子写盘；同名或同稳定 ID 节点覆盖，未涉及节点保留。

需要重置凭证时，只能通过独立 SSH 通道在 VPS 本机操作：

```bash
systemctl stop rs
rm -f /root/prod.rs-claim-url
/opt/rs/rs credential reset \
  --state /etc/rs/auth.json \
  --claim-url-file /root/prod.rs-claim-url
systemctl start rs
```

然后读取新链接，要求用户在本地终端重新执行 `claim`。用户确认成功后再继续验证。旧
账号、密码和 session 均已失效；客户端按稳定 ID 替换原节点。

安全升级已有服务时必须遵循 Skill 直接引用的升级参考，不得直接覆盖运行中的服务端
二进制。
