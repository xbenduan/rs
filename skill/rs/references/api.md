# rs latest 命令与协议

行为以 latest Release 的源码和 `CHANGELOG.md` 为准。

## `init`

```bash
rs init \
  --name prod \
  --endpoint https://rs.example.com \
  [--state .rs-auth.json] \
  [--claim-url-file /root/prod.rs-claim-url]
```

- `endpoint` 必须是纯 HTTPS origin。
- `name` 只能包含字母、数字、点、下划线和连字符，长度为 1 至 64。
- 密码始终为自动生成的 144 bit 高熵随机密钥，不接受人工输入。
- 账号 ID 为 128 bit，领取令牌为 256 bit。
- 节点凭证使用领取令牌经 AES-GCM 加密；state 只保存令牌哈希、nonce 和密文。
- 密码以 SHA-256 哈希保存；state 结构为版本 2，无 `password_salt` 字段。
- state 与 `claim-url-file` 均以 `0600` 原子写入，已有文件时拒绝覆盖。不存在的父目录
  以 `0700` 创建，已存在的父目录权限保持不变。

## `claim`

```bash
rs claim 'https://rs.example.com/claim/<one-time-token>'
```

省略位置参数时也可从隐藏终端或 stdin 读取链接。

- 只接受 HTTPS；HTTP 仅允许 loopback 测试。
- 客户端以 `POST` 请求链接；`GET`/`HEAD` 返回 405 且不消耗链接，链接预览与扫描器
  不会误领取。
- 服务端持锁校验令牌，先原子清除 state 中的领取字段，再返回密文信封。
- HTTP 响应只包含 `version`、`account_id`、`nonce` 和 `ciphertext`。
- 客户端使用 URL token 在本地 AES-GCM 解密，凭证不以 HTTP 明文返回。
- 客户端保存 claim 中的稳定节点 ID，并记录 HTTPS 连接的真实对端 IP；已有同名或同 ID
  节点被替换，IP 仅作观察记录，不参与合并。
- 命令不输出密码；并发请求最多一个成功，消费结果跨服务重启保持。
- Agent 获取链接后必须把完整命令交给用户，由用户在本地终端执行；用户确认成功后 Agent
  才继续节点检查与 Health。Agent 命令沙箱可能不能写入 `~/.rs`，不得代为领取。

## `serve`

```bash
rs serve \
  [--listen 127.0.0.1:8999] \
  [--state .rs-auth.json] \
  [--tls-cert FILE --tls-key FILE] \
  [--session-ttl 15m] \
  [--max-upload-size 104857600]
```

服务入口同时处理：

- `POST /claim/{token}`：消费单次链接并返回密文信封。
- `POST /r/{account-id}/session`：密码登录。
- HTTP/2 `application/grpc`：Shell、Upload、Health。

不存在公网凭证恢复接口。无 TLS 时程序仅允许监听 loopback，由 Caddy 通过 h2c 反代。
运行期间独占 `<state>.lock`，同一 state 不能同时启动第二个服务或执行凭证重置。
`SIGINT` 与 `SIGTERM` 均触发优雅退出。全部配置通过命令行参数传入，不读取环境变量。

## 节点管理

```bash
rs node add <name> <account-url>
rs node add --json
rs node list [--json]
rs node rm <name>
```

`node add` 从隐藏终端或 stdin 读取密码并完成真实登录，保存服务端返回的稳定节点 ID
和真实对端 IP。同名或同 ID 节点会被新节点替换；IP 不参与合并，共享代理或 CDN 后的
不同服务器不会互相覆盖。新建的节点目录权限为 `0700`、文件权限为 `0600`；Windows
密码使用当前用户 DPAPI 加密，无法解密时直接报错。纯读取不创建锁文件；新增、删除和
领取节点时由 Unix 目录锁或 Windows 命名互斥量保护读改写，最终使用临时文件和原子
rename 提交，不留下持久锁文件。

`node add --json` 从 stdin 读取严格的非空 JSON 数组。每项必须包含 `name`、`url` 和
`password`；客户端在线认证后自行写入 `id` 与真实 `ip`。仅在全部认证成功后原子更新
节点。

`node list --json` 输出：

```json
[
  {
    "name": "prod",
    "id": "<stable-node-id>",
    "ip": "203.0.113.10",
    "url": "https://rs.example.com/r/<account-id>"
  }
]
```

列表永远不输出密码。

## `update`

```bash
rs update
```

- 支持 macOS 与 Linux 的 `amd64`、`arm64` 客户端。
- 通过 GitHub `latest` Release 标签比较 `X.Y.Z` 版本；latest 不高于当前版本时不下载
  产物，也不写入文件。
- 有新版本时从对应 Release 下载 `SHA256SUMS` 和当前平台产物，SHA-256 严格匹配后才在
  当前二进制目录创建临时文件并原子替换。
- 下载、校验或替换失败时保留当前二进制，临时文件自动清理。
- Windows 为避免运行中替换留下 `.old` 或 `.new` 文件，命令明确拒绝自更新，应按安装
  参考手工下载并校验 latest Release。

## 登录和会话

客户端每次命令先发送：

```http
POST /r/{account-id}/session
Content-Type: application/json

{"password":"..."}
```

成功响应：

```json
{"token":"...","expires_at":"...","node_id":"<stable-node-id>","version":"<server-version>"}
```

- session 默认有效期 15 分钟，使用 HMAC-SHA256 签名。
- session TTL 最低为 1 秒。
- gRPC metadata 使用 `authorization: Bearer <token>`。
- 所有 RPC，包括 Health，都必须携带有效 session。
- 客户端根据登录响应校验主版本，主版本不同或版本格式无效时拒绝继续；已保存 ID 的
  节点登录到 ID 不同的服务端时同样拒绝。

## `shell`

```bash
rs shell <node> '<script>' [--timeout N]
```

- 脚本整段交给远端 `sh -c`；无脚本参数时从 stdin 读取。
- 默认超时 30 秒；Unix 服务端超时终止整个进程组。
- 远端退出码透传，stdout/stderr 分流；进程退出前写出的输出全部送达后才发送退出帧。
- 后台进程（如 `cmd &`）继续持有输出时，Shell 退出 5 秒后结束输出转发并返回 Shell
  自身的退出码。
- 子进程继承服务进程的环境变量。
- 未收到退出帧时返回 `connection closed before exit status`。

## `push`

```bash
rs push <node> <local> <absolute-remote> [--mkdir] [--overwrite] [--timeout N]
```

- 64 KiB 分块上传，目标必须是绝对路径。
- 默认使用原子 no-replace 提交拒绝覆盖；`--mkdir` 可创建父目录。
- 严格校验声明大小和 64 KiB 单块上限，`fsync`、`chmod 0644` 后提交。
- 默认上传上限为 100 MiB。
- 客户端完整调用默认 300 秒超时；服务端单次接收停滞 2 分钟或总时长超过 10 分钟后终止。

## `health`

```bash
rs health <node>
```

成功证明 DNS、TLS、账号密码、短期会话和 gRPC 全链路可用。首次安装还必须执行：

```bash
rs shell <node> 'hostname; uname -srmo; uptime'
```

## `credential reset`

该命令只操作 VPS 本机认证 state，不发起网络请求：

```bash
systemctl stop rs
rs credential reset \
  --state /etc/rs/auth.json \
  [--name prod] \
  [--claim-url-file /root/prod.rs-claim-url]
systemctl start rs
```

- 重建 account ID、密码、session key 和单次领取密文。
- 旧账号、密码与全部旧 session 立即失效。
- 未指定 `claim-url-file` 时链接输出到 stdout。
- 只读取现有 state 的 `endpoint`、`node_name` 与 `node_id`，保留节点 ID；`--name` 可替换
  节点名，state 缺少节点名时必须提供。
- `rs serve` 持有 state 锁时直接失败，必须先停止服务。
- 重启服务后要求用户在本地终端重新执行 `claim`，本地节点会按稳定 ID 替换。

## gRPC

```proto
service Rs {
  rpc Shell(ShellRequest) returns (stream ShellOutput);
  rpc Upload(stream UploadRequest) returns (UploadAck);
  rpc Health(HealthRequest) returns (HealthResponse);
}
```

- 服务端单条 gRPC 消息上限：1 MiB。
- TCP 建连上限：10 秒。
- TLS 最低版本：1.2。
- 客户端不提供跳过证书校验选项。
