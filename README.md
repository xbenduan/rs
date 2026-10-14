<div align="center">

# rs

**面向 AI Agent 的安全远程运维执行协议**

把远程认证、命令执行、文件传输和结果反馈收口到一个单二进制工具中，
让 Agent 以可预测、可验证的方式管理单台或少量 VPS。

[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/xbenduan/rs?label=release&sort=semver)](https://github.com/xbenduan/rs/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/xbenduan/rs)](go.mod)
[![Platforms](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey.svg)](#平台支持)

</div>

---

## ✨ 让 AI 安装

把下面这句话直接发给你的 Agent：

```text
安装 https://github.com/xbenduan/rs/releases/latest/download/skill.zip
```

Agent 会安装 Skill，从同一个 GitHub Release 下载并校验对应平台的二进制，然后完成本机客户端与远端 Linux 服务端部署。

## 目录

- [这是什么](#这是什么)
- [核心能力](#核心能力)
- [设计原则](#设计原则)
- [快速开始](#快速开始)
- [命令](#命令)
- [安全模型](#安全模型)
- [网络模型](#网络模型)
- [平台支持](#平台支持)
- [从源码构建](#从源码构建)
- [当前边界](#当前边界)
- [项目状态](#项目状态)
- [许可证](#许可证)

## 这是什么

**rs** 是面向 AI Agent 的安全远程运维执行协议。它把远程认证、命令执行、文件传输和结果反馈收口到一个单二进制工具中，让 Agent 能以可预测的方式管理单台或少量 VPS。

```text
Human
  │ natural language
  ▼
AI Agent
  │ plan · approval · verification
  ▼
rs client ───── HTTPS / gRPC ───── rs server
                                        │
                                        ▼
                                       VPS
```

`rs` 只负责可靠地执行操作，不内置模型、对话界面或 Agent 编排。上层 Agent 负责理解目标、判断风险和验证结果，VPS 无需保存任何模型凭证。

## 核心能力

| 能力 | 行为 |
|---|---|
| 节点管理 | 新节点使用稳定随机 ID，记录真实对端 IP 并支持安全 JSON 列表 |
| 一次性领取 | 用户在本地终端使用阅后即焚链接领取节点，客户端二进制解密密文 |
| 远程执行 | 通过 `sh -c` 执行非交互脚本，实时分离 stdout 与 stderr |
| 退出语义 | 透传远端退出码，连接中断不会被误报为成功 |
| 文件上传 | 分块传输、严格大小校验、超时回收和原子 no-replace |
| 健康检查 | 验证 DNS、TLS、登录、短期会话和 gRPC 全链路 |
| 本机重置 | 仅能在 VPS 本机重建全部凭证并生成新的单次领取链接 |

## 设计原则

### 单二进制

客户端与服务端使用同一个二进制。VPS 运行 `rs serve`，Agent 所在机器使用其余命令。发布产物覆盖 macOS、Linux 和 Windows 的 `amd64`、`arm64`。

### 非交互执行

命令必须在无人值守环境中结束，并通过退出码表达结果。`rs` 不提供 PTY、远程桌面或 Web 控制台，避免把终端交互的不确定性引入 Agent 工作流。

### 明确失败

Shell 输出、退出状态、认证错误和传输错误拥有独立语义。只有收到服务端的退出状态帧，客户端才会把命令视为正常结束。

### 默认安全

公网连接必须使用受信任的 HTTPS。未配置 TLS 的服务端只能监听 loopback，适合由 Caddy 等反向代理终止 TLS。

## 快速开始

> 推荐优先使用顶部的 [Agent 安装方式](#-让-ai-安装)。以下命令用于理解 `rs` 的基本工作流。

### 初始化服务端

```bash
/opt/rs/rs init \
  --name prod \
  --endpoint https://rs.example.com \
  --state /etc/rs/auth.json \
  --claim-url-file /root/prod.rs-claim-url
```

初始化不输出账号密码。指定 `--claim-url-file` 后，随机的一次性领取链接只写入该 `0600` 文件；省略时直接输出链接。待领取节点凭证使用链接中的 256 bit 随机令牌经 AES-GCM 加密，服务端认证状态只保存令牌哈希、nonce 和密文。

### 启动服务

```bash
/opt/rs/rs serve \
  --listen 127.0.0.1:8999 \
  --state /etc/rs/auth.json
```

Caddy 配置：

```caddyfile
rs.example.com {
  reverse_proxy h2c://127.0.0.1:8999
}
```

不要为该站点记录完整请求路径，否则一次性领取链接可能进入 access log。

### 领取凭证

服务启动且 HTTPS 生效后，由用户在自己的本地终端执行 Agent 提供的领取命令：

```bash
~/.local/bin/rs claim 'https://rs.example.com/claim/<one-time-token>'
```

用户确认领取成功后，再清理服务端链接文件：

```bash
ssh root@server 'rm -f /root/prod.rs-claim-url'
```

服务端先原子销毁待领取状态，再返回只含 nonce 与 ciphertext 的密文信封。`rs claim` 使用链接 token 在客户端本地解密，将节点以 `0600` 写入 `~/.rs/node.json`，并记录本次 HTTPS 连接的真实对端 IP。Agent 命令沙箱可能无法写入 `~/.rs`，因此 claim 应由用户本地执行。链接只可成功使用一次，不应提交 Git 或进入长期日志。

### 登记节点

已有账号 URL 与密码时，也可以手动登记：

```bash
printf '%s' '<password>' |
  rs node add prod \
  'https://rs.example.com/r/<account-id>'
```

`node add` 会先完成真实登录，记录服务端返回的稳定节点 ID 与连接的真实对端 IP。后续凭证重置按 ID 替换原节点；IP 只作观察记录，不参与合并。

也可以通过 stdin 提交节点 JSON 数组。所有节点验证成功后才会原子更新，同名节点覆盖，未涉及节点保留：

```bash
rs node add --json <<'JSON'
[
  {
    "name": "prod",
    "url": "https://rs.example.com/r/<account-id>",
    "password": "<password>"
  }
]
JSON
```

### 重置凭证

凭证只能在 VPS 本机重置。先停止服务，重建账号、密码、session key 和单次领取链接，再重启并重新领取：

```bash
ssh root@server '
  systemctl stop rs
  rm -f /root/prod.rs-claim-url
  /opt/rs/rs credential reset \
    --state /etc/rs/auth.json \
    --claim-url-file /root/prod.rs-claim-url
  systemctl start rs
'
# 用户在自己的本地终端执行 Agent 提供的新链接
~/.local/bin/rs claim 'https://rs.example.com/claim/<one-time-token>'
rs health prod
```

### 安全升级

升级时从 GitHub `latest` Release 下载并校验客户端与服务端产物。先在独立 loopback 端口启动候选服务并通过 HTTPS Health 验证，再切换正式服务；旧服务、旧二进制和 Caddy 备份必须保留到新服务重启验证完成。完整流程由随 Release 发布的 Skill 执行。

### 执行任务

```bash
rs health prod
rs shell prod 'uname -a && uptime'
rs push prod ./config.yaml /etc/my-app/config.yaml --mkdir --timeout 300
```

也可以通过标准输入提交多行脚本：

```bash
rs shell prod <<'SCRIPT'
set -eu
systemctl restart my-app
systemctl is-active my-app
SCRIPT
```

## 命令

```text
rs init --name NAME --endpoint https://host [--state FILE] [--claim-url-file FILE]
rs claim [one-time-url]
rs serve [--listen ADDR] [--state FILE] [--tls-cert FILE --tls-key FILE]
rs node add <name> <account-url>
rs node add --json
rs node list [--json]
rs node rm <name>
rs shell <node> "<script>" [--timeout N]
rs push <node> <local> <remote> [--mkdir] [--overwrite] [--timeout N]
rs health <node>
rs credential reset [--state FILE] [--name NAME] [--claim-url-file FILE]
```

## 安全模型

- 凭证始终为服务端自动生成的 144 bit 高熵随机密钥，不接受人工输入。
- 密码使用 SHA-256 哈希保存，服务端不保存密码明文；高熵凭证下无需慢哈希或加盐。
- 待领取凭证使用一次性 URL 中的随机令牌经 AES-GCM 加密，服务端不保存令牌和待领取凭证明文。
- 领取响应只包含密文信封；凭证只在客户端二进制内解密，不以 HTTP 明文返回。
- 领取时先原子清除服务端密文再返回响应；并发请求最多一个成功，服务重启后仍保持失效。
- 每次操作先通过 HTTPS 登录，再使用短期 HMAC-SHA256 会话调用 gRPC。
- 所有 RPC，包括 `Health`，都要求有效会话。
- 一次性领取只接受 `POST`；链接预览、扫描器的 `GET`/`HEAD` 访问不会消耗链接。
- 登录响应携带节点 ID 与服务端版本；主版本不兼容或节点 ID 与本地记录不一致时，客户端拒绝执行远程操作。
- `rs serve` 运行期间独占认证 state 锁，`credential reset` 必须先停止服务。
- Unix 服务端在命令超时时终止整个进程组。
- 文件上传默认以原子 no-replace 提交拒绝覆盖，并通过严格长度校验和 `fsync` 落盘。
- Windows 客户端使用当前用户 DPAPI 加密节点文件中的密码；macOS 与 Linux 依赖 `0600` 文件权限（新建目录为 `0700`，已存在目录权限保持不变）。

> ⚠️ 以 `root` 运行服务端时，账号 URL 与密码的组合等价于该 VPS 的 root 运维权限。当前版本采用单账号模型，适合个人开发者管理自己信任的服务器，不适合多个互不信任的操作人员共享。
>
> Agent 可以读取一次性链接并生成命令，但 `rs claim` 应由用户在本地终端执行。链接在消费前仍等同临时最高权限凭证，不能对抗已经拥有 VPS root 权限或能够截获链接的攻击者。

## 网络模型

推荐部署拓扑：

```text
Internet :443
      │
      ▼
Caddy / trusted TLS proxy
      │ h2c
      ▼
127.0.0.1:8999
      │
      ▼
rs server
```

也可以使用 `--tls-cert` 与 `--tls-key` 让 `rs` 直接监听 HTTPS。客户端不提供跳过证书校验的选项。

## 平台支持

| 平台 | amd64 | arm64 | 用途 |
|---|:---:|:---:|---|
| macOS | ✅ | ✅ | 客户端 |
| Linux | ✅ | ✅ | 客户端与服务端 |
| Windows | ✅ | ✅ | 客户端 |

> macOS 自带 `/usr/bin/rs`，它不是本项目。建议将客户端安装为 `~/.local/bin/rs` 并确认该路径优先于 `/usr/bin`。

## 从源码构建

需要 Go `1.26.9` 或更高版本：

```bash
go build -o rs .
go test ./...
go test -race ./...
go vet ./...
govulncheck ./...
```

生成全部发布产物：

```bash
./scripts/release.sh
```

构建结果写入被 Git 忽略的 `releases/`，正式二进制只通过 [GitHub Releases](https://github.com/xbenduan/rs/releases) 分发。

## 当前边界

latest 版本尚未提供：

- 文件下载和目录同步
- 交互式终端或 PTY
- 除节点列表外的 JSON 输出与稳定错误码
- 异步任务、日志续查和执行历史
- 命令级授权、风险策略和人工审批
- 自动升级与回滚
- macOS Keychain 与 Linux Secret Service 集成

这些能力会围绕 Agent 运维的可理解、可约束和可验证性逐步扩展。交互式远程桌面和大型监控平台不属于近期方向。

## 项目状态

当前版本以 `server/server.go` 中的 `Version` 为唯一来源；安装与升级始终使用 [GitHub latest Release](https://github.com/xbenduan/rs/releases/latest)，历史变化见 [CHANGELOG.md](CHANGELOG.md)。

## 许可证

本项目基于 [MIT License](LICENSE) 开源。
