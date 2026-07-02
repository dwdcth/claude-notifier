# claude-notifier（中文使用手册）

> 本项目 fork 自 [felipeelias/claude-notifier](https://github.com/felipeelias/claude-notifier)，在其基础上合并了 [yuuichieguchi/claude-remote-approver](https://github.com/yuuichieguchi/claude-remote-approver) 的远程审批功能，并修复了若干 bug。

## 特性

- 多通道通知分发（ntfy、terminal-notifier）
- **远程审批**：Claude Code 权限请求推送到 ntfy，手机点 Approve / Deny / Always Approve
- **AskUserQuestion 远程联动**：ntfy 上点选项，答案通过 `decision.updatedInput` 回传 Claude Code，跳过本地 UI
- **Stop 通知**：对话结束时通知，含最后 user prompt + assistant reply（各截 50 字）
- **idle_prompt 增强**：闲置通知也含 prompt / reply
- **通知去重**：sha256 + 时间窗口，避免 Stop + idle 重复
- **UTF-8 安全截断**：rune-based，中文不会乱码
- **XDG 配置**：默认 `~/.config/claude-notifier/config.toml`
- 跨平台单 binary（Linux / macOS / Windows，amd64 / arm64）
- 永不失败（hook 始终 exit 0，不会破坏 Claude Code 流程）

## 安装

### 从源码编译（需要 Go 1.24+）

```bash
git clone https://github.com/dwdcth/claude-notifier.git
cd claude-notifier
go build -o ~/bin/claude-notifier .
```

把 `~/bin` 加到 `PATH` 即可直接使用 `claude-notifier` 命令。

## 配置

### 初始化配置文件

```bash
claude-notifier init
```

会在 `~/.config/claude-notifier/config.toml` 创建默认配置。

### 手动配置示例

```toml
[global]
timeout = "10s"

[approver]
server = "https://ntfy.sh"
topic = "your-secret-topic"   # 自己生成，建议 cra- 前缀 + 32 hex
timeout = "120s"

[[notifiers.ntfy]]
url = "https://ntfy.sh/your-secret-topic"
title = "Claude Code ({{.Project}}) {{.NotificationType}}"
message = "{{.Message}}\n\n📁 {{.Cwd}}"
priority = "default"
tags = "robot"
```

## Claude Code Hooks 配置

在 `~/.claude/settings.json`（全局）或项目 `.claude/settings.local.json` 中加入：

```json
{
  "hooks": {
    "Notification": [
      { "hooks": [{ "type": "command", "command": "/path/to/claude-notifier" }] }
    ],
    "PermissionRequest": [
      { "hooks": [{ "type": "command", "command": "/path/to/claude-notifier hook" }] }
    ],
    "Stop": [
      { "hooks": [{ "type": "command", "command": "/path/to/claude-notifier stop" }] }
    ]
  }
}
```

> 或者用 `claude-notifier setup` 自动注册 PermissionRequest hook（写入 `~/.claude/settings.json`）。

## 模板变量

| 变量 | 含义 |
|---|---|
| `{{.Message}}` | Claude 通知消息（Stop / idle 会含 prompt / reply 摘要） |
| `{{.Title}}` | 通知标题 |
| `{{.Project}}` | 项目名（cwd 最后一段） |
| `{{.Cwd}}` | 工作目录 |
| `{{.NotificationType}}` | `permission_prompt` / `idle_prompt` / `auth_success` / `elicitation_dialog` / `stop` |
| `{{.SessionID}}` | Claude Code session ID |
| `{{.TranscriptPath}}` | 对话记录文件路径 |

## 命令列表

| 命令 | 说明 |
|---|---|
| `claude-notifier` | 默认：读 stdin JSON，分发到所有 notifiers（Notification hook 用） |
| `claude-notifier hook` | 处理 PermissionRequest hook（远程审批） |
| `claude-notifier stop` | 处理 Stop hook（对话结束通知，含 prompt / reply） |
| `claude-notifier init` | 创建默认配置文件 |
| `claude-notifier test` | 发测试通知 |
| `claude-notifier setup` | 配置远程审批（生成 topic + 注册 PermissionRequest hook） |
| `claude-notifier status` | 查看远程审批状态 |
| `claude-notifier enable` | 启用审批 hook |
| `claude-notifier disable` | 禁用审批 hook |
| `claude-notifier uninstall` | 移除审批 hook 和 approver 配置 |

## 远程审批原理

```
Claude Code
  │
  │  PermissionRequest hook (stdin JSON)
  ▼
claude-notifier hook
  │
  ├──POST──▶ ntfy.sh/<topic>          ──push──▶  手机 / 网页 ntfy
  │                                                 │
  │                                     Approve / Deny / 选项 点击
  │                                                 │
  └──SSE───▶ ntfy.sh/<topic>-response  ◀──POST──┘
  │
  │  stdout JSON: allow / deny / ask
  ▼
Claude Code 继续或停止
```

- Approve / Deny：HTTP action POST `{requestId, decision}` 到 `<topic>-response`
- AskUserQuestion 选项：POST `{requestId, answer}` 到 `<topic>-response`
- hook 收到匹配 requestId 的响应 → 返回 `allow + updatedInput`（AskUserQuestion 把 answers 回传）
- 超时 / 出错 → 返回 `ask`，Claude Code 走本地对话框

## 通知去重

- 同 sessionID + 同 Message 内容 + 2 分钟内 → 自动跳过
- 状态文件：`$XDG_CACHE_HOME/claude-notifier/dedup.json`（默认 `~/.cache/claude-notifier/dedup.json`）

## 隐私提示

公共 ntfy.sh 服务器会经过第三方。敏感工作建议 [自托管 ntfy](https://docs.ntfy.sh/install/)，把配置里的 `server` 指向自己的实例。

---

# Original README (English)

[![CI](https://github.com/felipeelias/claude-notifier/actions/workflows/ci.yml/badge.svg)](https://github.com/felipeelias/claude-notifier/actions/workflows/ci.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/felipeelias/claude-notifier)](https://github.com/felipeelias/claude-notifier/blob/main/go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/felipeelias/claude-notifier/blob/main/LICENSE)

Notification dispatcher and **remote permission approver** for [Claude Code](https://docs.anthropic.com/en/docs/claude-code) hooks. Reads JSON from stdin, fans out to all configured notification channels concurrently. Also supports remote approval of Claude Code permission requests via ntfy push notifications. Single static binary, compiled-in plugins, TOML configuration.

## Why

Claude Code has [notification hooks](https://docs.anthropic.com/en/docs/claude-code/hooks) that run a shell command when the agent needs your attention. Most people write a bash script that curls ntfy or sends a desktop notification. That works fine for one channel on one machine.

It gets annoying when you want notifications on your phone *and* your desktop, or you move to a different OS and have to rewrite the script, or you want high priority for errors but low priority for routine updates.

claude-notifier is a single binary that handles all of that:

- Sends to multiple channels from one hook (ntfy, desktop, Slack, etc.)
- Same binary and config file across Linux, macOS, and Windows
- Always exits 0 so it never breaks your hook
- **Remote approval**: approve/deny Claude Code permission requests from your phone
- `claude-notifier init`, edit the TOML, you're done

## Install

### Homebrew (macOS and Linux)

```bash
brew install felipeelias/tap/claude-notifier
```

### Debian / Ubuntu

Download the `.deb` from [GitHub Releases](https://github.com/felipeelias/claude-notifier/releases) and install:

```bash
sudo dpkg -i claude-notifier_*.deb
```

### Fedora / RHEL

Download the `.rpm` from [GitHub Releases](https://github.com/felipeelias/claude-notifier/releases) and install:

```bash
sudo rpm -i claude-notifier_*.rpm
```

### Manual download

Pre-built binaries for macOS, Linux, and Windows (amd64 and arm64) are available on the [GitHub Releases](https://github.com/felipeelias/claude-notifier/releases) page. Download the archive for your platform, extract it, and place the binary in your `PATH`.

### From source

Requires Go 1.24+.

```bash
go install github.com/felipeelias/claude-notifier@latest
```

## Setup

Initialize the config file:

```bash
claude-notifier init
```

This creates `~/.config/claude-notifier/config.toml`. Edit it to configure your notification channels.

### Claude Code hook

Add to your Claude Code settings (`~/.claude/settings.json`):

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

Or install the Claude plugin which configures the hook automatically.

### Remote approval setup

To enable remote approval of Claude Code permission requests via ntfy:

```bash
claude-notifier setup
```

This will:
1. Generate a random ntfy topic name
2. Save the approver configuration to your config file
3. Register the `PermissionRequest` hook in `~/.claude/settings.json`

Subscribe to the generated topic in your ntfy mobile app to receive approval requests. When Claude Code needs permission, you'll get a push notification with Approve/Deny buttons.

## Configuration

Run `claude-notifier init` to generate a config file with all available options documented. See [`config.example.toml`](config.example.toml) for the full reference.

## Template variables

Plugins that support Go templates (like ntfy) have access to the following variables from the Claude Code [Notification hook](https://docs.anthropic.com/en/docs/claude-code/hooks) payload:

| Variable                | Source                              |
| ----------------------- | ----------------------------------- |
| `{{.Message}}`          | Notification message from Claude    |
| `{{.Title}}`            | Notification title from Claude      |
| `{{.Project}}`          | Project name (last segment of cwd)  |
| `{{.Cwd}}`              | Working directory                   |
| `{{.NotificationType}}` | `permission_prompt`, `idle_prompt`, `auth_success`, `elicitation_dialog` |
| `{{.SessionID}}`        | Claude Code session ID              |
| `{{.TranscriptPath}}`   | Path to conversation transcript     |

Plugins can also define custom variables via their config (e.g., `[notifiers.ntfy.vars]`). User-defined keys are title-cased for template access (`env` becomes `{{.Env}}`).

## Usage

The primary use case is as a Claude Code hook — it reads JSON from stdin and dispatches to all configured notifiers:

```bash
echo '{"message":"Build complete","title":"Claude Code"}' | claude-notifier
```

### Commands

| Command                     | Description                                     |
| --------------------------- | ----------------------------------------------- |
| `claude-notifier`           | Read JSON from stdin, dispatch to all notifiers |
| `claude-notifier init`      | Create default config file                      |
| `claude-notifier test`      | Send a test notification to all notifiers       |
| `claude-notifier setup`     | Configure remote approval with ntfy             |
| `claude-notifier status`    | Show remote approval configuration              |
| `claude-notifier enable`    | Register the approval hook                      |
| `claude-notifier disable`   | Unregister the approval hook                    |
| `claude-notifier uninstall` | Remove approval hook and config                 |
| `claude-notifier --version` | Print version                                   |

### Flags

| Flag             | Env                      | Description                                                            |
| ---------------- | ------------------------ | ---------------------------------------------------------------------- |
| `--config`, `-c` | `CLAUDE_NOTIFIER_CONFIG` | Path to config file (default: `~/.config/claude-notifier/config.toml`) |

## Plugins

Each plugin is configured under `[[notifiers.<name>]]` in the config file. Run `claude-notifier init` to generate a config with all plugins and their options documented.

| Plugin | Description |
| ------ | ----------- |
| [ntfy](https://ntfy.sh) | HTTP-based push notifications |
| [terminal-notifier](https://github.com/julienXX/terminal-notifier) | macOS desktop notifications |

Want to add a plugin? See [CONTRIBUTING.md](CONTRIBUTING.md).

## Inspiration

- [Telegraf](https://github.com/influxdata/telegraf) by InfluxData — plugin architecture, TOML config with `[[section]]` arrays, and the `init()` registry pattern
- [ntfy](https://ntfy.sh) by Philipp C. Heckel — simple, self-hostable push notifications

## License

MIT
