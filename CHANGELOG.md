# Changelog

本文件记录 `rs` 的版本历史。版本号唯一来源是 `server/server.go` 中的 `Version`。

## v1.1.0 - 2026-10-14

### 功能

- 新增 `rs update`，自动检查 GitHub latest Release；仅在发现更高版本时下载当前平台产物，
  严格校验 `SHA256SUMS` 后原子替换当前客户端。
- macOS 与 Linux 支持客户端自更新；Windows 为避免运行中替换产生残留文件，继续使用
  latest Release 手工校验安装。

### 变更

- 节点读取不再创建或持有 `<node.json>.lock`；`node list`、`health`、`shell` 和 `push`
  只读本地节点文件，不产生任何本地文件写入。
- 节点新增、删除和领取改用 Unix 目录锁或 Windows 命名互斥量配合原子替换保护并发写入，
  不再创建持久锁文件。

## v1.0.0 - 2026-10-14

首个正式发行版本。

### 功能

- 单二进制客户端与服务端，覆盖 macOS、Linux、Windows 的 `amd64` 与 `arm64`。
- 节点管理：稳定随机节点 ID，记录真实对端 IP，`rs node list --json` 安全输出不含密码。
- 一次性领取：阅后即焚链接经 AES-GCM 加密分发凭证，服务端只保存令牌哈希与密文信封，
  客户端二进制本地解密，领取只接受 `POST` 且成功一次后原子失效。
- 远程执行：`rs shell` 通过 `sh -c` 执行非交互脚本，实时分离 stdout/stderr，透传远端
  退出码，连接中断不会被误报为成功。
- 文件上传：`rs push` 分块传输、严格长度校验、`fsync` 落盘、原子 no-replace 提交。
- 健康检查：`rs health` 验证 DNS、TLS、登录、短期会话与 gRPC 全链路。
- 本机重置：`rs credential reset` 仅能在 VPS 本机重建全部凭证并生成新的单次领取链接。
- 发布脚本完成检查和全平台构建后，自动创建版本标签、GitHub Release 并上传全部产物。

### 安全

- 凭证为服务端生成的高熵随机密钥，密码以 SHA-256 哈希保存，不保存明文。
- 每次操作先经 HTTPS 登录，再使用短期 HMAC-SHA256 会话调用 gRPC；所有 RPC 均需有效会话。
- 登录响应携带节点 ID 与服务端版本；主版本不兼容或节点 ID 不一致时客户端拒绝执行。
- `rs serve` 运行期间独占认证 state 锁，`credential reset` 必须先停止服务。
- Unix 服务端在命令超时时终止整个进程组。
- Windows 客户端使用 DPAPI 加密节点密码；macOS 与 Linux 依赖 `0600` 文件权限。
