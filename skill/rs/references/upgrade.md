# rs 安全升级

升级目标是先证明新链路可用，再停止旧服务。全程保留 SSH 回滚通道，候选端口只监听
loopback，不新增公网防火墙规则。

## 前置检查

1. 运行当前客户端的 `node list`、`health <node>` 和一条只读 Shell。
2. 确认存在可用 SSH 登录方式；只有 rs 通道而没有 SSH 时不得升级服务端。
3. 记录当前客户端和服务端版本、远端架构、systemd unit、监听端口与 Caddy 路由。
4. 检查 `CHANGELOG.md`。若 latest 的主版本与当前主版本不同，必须按发布说明执行状态迁移；
   没有明确迁移步骤的跨主版本升级应停止。

## 获取并校验 latest

固定下载根地址：

```text
https://github.com/xbenduan/rs/releases/latest/download
```

下载 `SHA256SUMS`、本机客户端产物和远端 Linux 架构产物。逐个计算 SHA-256 并与清单
严格匹配，校验前不得执行。客户端暂存为 `~/.local/bin/rs.new`，服务端产物通过当前 rs
上传到 `/opt/rs-upgrade/rs`，权限设为 `0755`。运行两个暂存二进制的无参数输出，确认
版本一致。

不要覆盖 `~/.local/bin/rs` 或 `/opt/rs/rs`。

## 启动候选服务

1. 选择未监听的 loopback 端口，例如 `127.0.0.1:8998`。
2. 创建 `/etc/rs-upgrade/`，将当前认证状态复制为
   `/etc/rs-upgrade/auth.json`，目录权限 `0700`、文件权限 `0600`。
3. 创建临时 `rs-upgrade.service`，使用 `/opt/rs-upgrade/rs`、候选端口和认证状态副本。
4. `systemctl daemon-reload` 后启动候选服务，确认 active、版本正确且只监听 loopback。
5. 此阶段禁止执行一次性凭证领取或凭证重置，避免正式状态与候选副本分叉。

旧 `rs.service` 必须保持 active。

## 切换候选流量

1. 备份 `/etc/caddy/Caddyfile`。
2. 只把 rs 的 h2c upstream 改为候选端口，生成候选配置。
3. 运行 `caddy validate`；成功后原子替换并 reload。
4. 使用 `~/.local/bin/rs.new` 和现有节点文件执行：

```bash
~/.local/bin/rs.new health <node>
~/.local/bin/rs.new shell <node> 'printf rs-upgrade-ok'
```

任一验证失败时，立即恢复 Caddy 备份并 reload。确认旧 Health 恢复后停止并删除候选
unit，不得覆盖正式二进制。

## 提升为正式服务

候选公网验证成功后：

1. 将 `/opt/rs/rs` 备份为 `/opt/rs/rs.prev`，保留原权限。
2. 停止正式 `rs.service`，将已校验的候选二进制原子安装到 `/opt/rs/rs`。
3. 启动正式服务，确认 active、版本正确且监听原 loopback 端口。
4. 将 Caddy upstream 改回正式端口，validate、原子替换并 reload。
5. 使用新客户端执行 Health 与 Shell，然后重启一次正式服务并再次执行 Health。
6. 最后将 `~/.local/bin/rs` 备份为 `rs.prev`，再原子安装 `rs.new`。

只有最终验证全部成功，才停止并删除 `rs-upgrade.service`、`/opt/rs-upgrade/` 和
`/etc/rs-upgrade/`。`/opt/rs/rs.prev` 至少保留到本次维护结束。

## 回滚

正式服务无法启动或最终 Health 失败时：

1. 保持 Caddy 指向仍可用的候选服务。
2. 用 `/opt/rs/rs.prev` 恢复 `/opt/rs/rs`，启动正式服务。
3. 确认旧正式服务 active 后，将 Caddy upstream 恢复到正式端口并 reload。
4. 使用旧客户端执行 Health。
5. 保留失败的新二进制和日志用于分析，不得删除认证状态。

若候选服务也不可用，立即通过 SSH 恢复 Caddy 备份、旧二进制和旧 unit。不得通过开放
明文公网端口、跳过 TLS 校验或删除认证状态来恢复连接。
