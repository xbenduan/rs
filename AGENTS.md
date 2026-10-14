# rs 开发约定

## 项目定位

`rs` 是面向 AI Agent 的单二进制远程运维执行协议。服务端运行在 VPS，客户端负责认证、
执行非交互 Shell、传输文件和健康检查。安全边界与命令行为以源码和 `README.md` 为准。

## 版本唯一来源

- 版本号唯一来源是 `server/server.go` 中的 `Version`。
- 不要在构建脚本中维护第二份版本号。

## 指导文件版本引用

- `README.md`、`AGENTS.md`、`skill/rs/SKILL.md` 与 `skill/rs/references/` 中面向用户或
  Agent 的安装、升级、下载链接统一使用 GitHub `latest` 入口，不得硬编码具体版本。
- 具体版本号只允许出现在 `server/server.go`、`CHANGELOG.md`、Git 标签、GitHub Release
  标题与发布说明，以及确实用于兼容性判断的历史记录或测试中。
- 下载地址统一使用
  `https://github.com/xbenduan/rs/releases/latest/download/<asset>`。

## 每次版本迭代

每次发布新版本必须完整执行以下步骤：

1. 更新 `server/server.go` 中的 `Version`。
2. 在 `CHANGELOG.md` 顶部新增对应的 `## vX.Y.Z - YYYY-MM-DD` 条目，说明新增、变更、修复及不兼容项。
3. 同步更新 `README.md`、`skill/rs/SKILL.md` 和 `skill/rs/references/` 中涉及命令、
   协议、安装或升级路径的内容，并保持全部下载指导使用 `latest`。
4. 运行 `gofmt -w`、`go test ./...` 和 `go vet ./...`。
5. 运行 `./scripts/release.sh`，重新生成全部发布产物，禁止只替换单个平台文件。
6. 确认 `releases/` 至少包含：
   - `rs-darwin-amd64`
   - `rs-darwin-arm64`
   - `rs-linux-amd64`
   - `rs-linux-arm64`
   - `rs-windows-amd64.exe`
   - `rs-windows-arm64.exe`
   - `skill.zip`
   - `SHA256SUMS`
7. 使用 `(cd releases && shasum -a 256 -c SHA256SUMS)` 校验产物。
8. 提交源码、技能和更新日志。不得 `git add -f releases/`，不得把任何发布二进制写入
   Git 历史。
9. 发布 GitHub Release 时使用相同版本号的 `vX.Y.Z` 标签，以 `CHANGELOG.md` 对应版本
   作为发布说明，并上传 `releases/` 中的全部文件。

## 发布约束

- 发布暂存目录固定为 `releases/`，并由 `.gitignore` 整体忽略。
- macOS、Linux、Windows 必须同时发布 `amd64` 和 `arm64`。
- 技能源文件固定为 `skill/rs/`，发布文件固定命名为 `skill.zip`，ZIP 内必须保留顶层 `rs/` 目录。
- 不得手工编辑 `SHA256SUMS`，也不得提交未经过测试的二进制。
- Windows 产物主要作为客户端使用；VPS 服务端部署目标仍是 Linux。
