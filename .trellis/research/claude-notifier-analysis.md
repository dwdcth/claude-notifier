# Research: claude-notifier (felipeelias/claude-notifier)

- **Query**: Full analysis of https://github.com/felipeelias/claude-notifier
- **Scope**: external
- **Date**: 2026-05-22

## Findings

### Overview

claude-notifier 是一个用 Go 编写的 **Claude Code hooks 通知分发器**。它从 stdin 读取 Claude Code 发送的 JSON 通知，并并发地分发到所有已配置的通知渠道。核心设计原则：单个静态二进制文件、编译时插件、TOML 配置。

### Purpose & Features

1. **多渠道通知**：一个 hook 同时发送到 ntfy、桌面通知等多个渠道
2. **跨平台**：同一二进制文件和配置文件在 Linux、macOS、Windows 上运行
3. **永不失败**：始终退出 0，不会破坏 Claude Code hook
4. **简单配置**：`claude-notifier init` 生成 TOML 配置文件，编辑即可
5. **Go 模板**：支持在消息/标题中使用 Go template 变量
6. **并发分发**：使用 `sync.WaitGroup` 并行发送到所有通知器
7. **超时控制**：全局超时设置（默认 10s），防止单个通知器阻塞

### Project Structure (All Files)

```
claude-notifier/
├── main.go                                    # 入口点，注册插件，启动 CLI
├── go.mod                                     # Go 1.26, 依赖: BurntSushi/toml, stretchr/testify, urfave/cli/v2
├── go.sum
├── integration_test.go                        # 端到端测试：构建二进制并测试完整流程
├── config.example.toml                        # 完整配置参考
├── README.md                                  # 文档
├── CONTRIBUTING.md                            # 贡献指南
├── LICENSE                                    # MIT
├── CODE_OF_CONDUCT.md
├── SECURITY.md
├── .goreleaser.yml                            # 跨平台构建 (linux/darwin/windows, amd64/arm64)
├── .golangci.yml                              # golangci-lint v2 配置
├── .gitignore
├── .markdownlint-cli2.yaml
├── .tool-versions                             # asdf 版本管理
├── .yamllint.yml
├── docs/
│   └── ROADMAP.md                             # 路线图：notify-send, Slack, Discord, Pushover, Sound 插件
├── internal/
│   ├── cli/
│   │   ├── cli.go                             # CLI 框架 (urfave/cli/v2)，命令：默认/send、init、test
│   │   └── cli_test.go
│   ├── config/
│   │   ├── config.go                          # TOML 配置加载，SampleConfig 生成
│   │   └── config_test.go
│   ├── dispatch/
│   │   ├── dispatch.go                        # 并发分发逻辑 (sync.WaitGroup)
│   │   └── dispatch_test.go
│   ├── notifier/
│   │   ├── notifier.go                        # Notification 结构体 + Notifier 接口
│   │   ├── notifier_test.go
│   │   ├── registry.go                        # 插件注册表 (Factory 模式)
│   │   └── registry_test.go
│   └── tmpl/
│       ├── tmpl.go                            # Go template 渲染工具
│       └── tmpl_test.go
├── plugins/
│   ├── ntfy/
│   │   ├── ntfy.go                            # ntfy.sh HTTP 推送通知插件
│   │   └── ntfy_test.go
│   └── terminalnotifier/
│       ├── terminalnotifier.go                # macOS terminal-notifier 桌面通知插件
│       └── terminalnotifier_test.go
├── .claude/
│   └── skills/
│       └── release/
│           └── SKILL.md                       # Claude Code release skill 定义
├── .github/
│   ├── workflows/
│   │   ├── ci.yml                             # CI: go test + vet + markdownlint + yamllint + golangci-lint
│   │   ├── release.yml                        # 发布: goreleaser + build provenance
│   │   └── dependabot-auto-merge.yml
│   ├── ISSUE_TEMPLATE/
│   │   ├── bug.yml
│   │   ├── config.yml
│   │   └── feature.yml
│   └── PULL_REQUEST_TEMPLATE.md
```

### Architecture

#### Entry Point: `main.go`

```go
func main() {
    reg := notifier.NewRegistry()
    ntfy.Register(reg)                    // 注册 ntfy 插件
    terminalnotifier.Register(reg)        // 注册 terminal-notifier 插件
    app := appcli.New(version, reg)
    err := app.Run(os.Args)
}
```

- 创建 Registry，注册所有插件
- 使用 `urfave/cli/v2` 作为 CLI 框架
- version 通过 ldflags 在构建时注入

#### Data Flow

```
Claude Code (hook) -> stdin (JSON) -> cli.sendAction()
  -> json.Decode -> Notification struct -> Validate()
  -> config.Load() -> loadNotifiers() (每个注册插件工厂创建实例)
  -> dispatch.Send() (并发发送到所有通知器)
```

#### Key Interfaces

**`notifier.Notifier`** (核心接口):
```go
type Notifier interface {
    Name() string
    Send(ctx context.Context, n Notification) error
}
```

**`notifier.Notification`** (Claude Code hook payload):
```go
type Notification struct {
    Message          string `json:"message"`
    Title            string `json:"title"`
    Cwd              string `json:"cwd"`
    NotificationType string `json:"notification_type"`
    SessionID        string `json:"session_id"`
    TranscriptPath   string `json:"transcript_path"`
}
```

- `Project()` 方法从 `Cwd` 提取最后一段路径

#### Plugin System (Registry Pattern)

- `Registry` 维护 `map[string]Factory`
- 每个 plugin 在 `init()` 或 `Register()` 中注册自己
- `Factory` 函数创建带默认值的新实例
- 配置通过 `toml.PrimitiveDecode` 反序列化到 plugin struct

#### Template System (`internal/tmpl/`)

- `BuildContext()`: 从 Notification + 用户 vars 构建模板上下文 map
- 用户 vars key 自动 Title-Case (如 `env` -> `{{.Env}}`)
- Claude Code 字段优先级高于用户 vars（碰撞时用户 vars 被忽略）
- `Render()`: 使用 Go `text/template` 渲染，`missingkey=error` 模式

#### Dispatch (`internal/dispatch/`)

- 使用 `sync.WaitGroup` 并发发送
- 收集所有错误返回
- 支持 `context.Context` 超时

### Plugins

#### ntfy (`plugins/ntfy/`)

- HTTP POST 到 ntfy 服务器
- 支持: priority, tags, icon, click, attach, filename, email, delay, actions, markdown
- 认证: Token (Bearer) 或 Basic Auth (用户名密码)
- Go template 渲染 message 和 title
- 默认值: `markdown=true`, `message="{{.Message}}"`, `title="Claude Code ({{.Project}})"`

#### terminal-notifier (`plugins/terminalnotifier/`)

- 通过执行 `terminal-notifier` 二进制文件发送 macOS 桌面通知
- Go template 渲染 message, title, subtitle, group
- 安全限制: `open` 和 `execute` 参数不进行模板渲染（防止注入）
- 默认 group 为 `{{.SessionID}}`（同一会话通知互相替换）
- 支持所有 terminal-notifier 参数: sound, activate, sender, appIcon, contentImage, ignoreDnD

### Claude Code Integration

#### Hook Configuration

在 `~/.claude/settings.json` 中配置:

```json
{
  "hooks": {
    "Notification": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "claude-notifier"
          }
        ]
      }
    ]
  }
}
```

- 注册为 Claude Code 的 "Notification" hook
- Claude Code 将通知 JSON 通过 stdin 传递给 `claude-notifier`
- 支持的通知类型: `permission_prompt`, `idle_prompt`, `auth_success`, `elicitation_dialog`

#### Notification Types (from Claude Code)

| Type | Description |
|---|---|
| `permission_prompt` | 需要用户授权 |
| `idle_prompt` | Agent 空闲等待 |
| `auth_success` | 认证成功 |
| `elicitation_dialog` | 需要用户输入 |

### Dependencies & Tech Stack

| Dependency | Version | Purpose |
|---|---|---|
| Go | 1.26 | 语言 |
| BurntSushi/toml | v1.6.0 | TOML 配置解析 |
| urfave/cli/v2 | v2.27.7 | CLI 框架 |
| stretchr/testify | v1.11.1 | 测试断言 |
| goreleaser | v2 | 跨平台构建发布 |
| golangci-lint | v2.10.1 | 代码检查 |

### Build & Release

- **goreleaser**: 构建 linux/darwin/windows (amd64/arm64) 二进制
- **输出格式**: tar.gz, deb, rpm, Homebrew tap
- **CI**: GitHub Actions - go test + vet + markdownlint + yamllint + golangci-lint
- **Release**: 推送 tag 触发 goreleaser，自动更新 Homebrew tap
- **ldflags**: `-s -w -X main.version={{.Version}}` 注入版本号

### Configuration Details

- 默认路径: `~/.config/claude-notifier/config.toml`
- 环境变量覆盖: `CLAUDE_NOTIFIER_CONFIG`
- `--config` / `-c` 命令行参数覆盖
- 全局超时: 默认 10s
- 输入大小限制: 1 MiB
- 字段长度限制: Message 4096, Title 256, Cwd 4096, NotificationType 64, SessionID 128, TranscriptPath 4096

### Roadmap (Planned Plugins)

- **notify-send**: Linux 桌面通知
- **Slack webhook**: Slack 消息
- **Discord webhook**: Discord 消息
- **Pushover**: 推送通知
- **Sound**: 播放声音

### Design Principles

1. **永不失败**: 所有错误都 log 并返回 nil，确保 hook 不被中断
2. **插件隔离**: 每个 plugin 是独立的 Go package，实现统一接口
3. **配置驱动**: 所有行为通过 TOML 配置，支持模板渲染
4. **安全优先**: 输入验证（大小限制）、terminal-notifier 的 execute/open 不模板化
5. **测试覆盖**: 每个 package 都有对应测试，集成测试构建实际二进制

## Caveats / Not Found

- 目前只有 2 个插件（ntfy + terminal-notifier），Slack/Discord/Pushover/notify-send/Sound 都在路线图中
- terminal-notifier 仅适用于 macOS
- 版本 v0.1.2（截至 2026-02-24）
- 没有热重载配置的功能
- 没有日志级别配置
