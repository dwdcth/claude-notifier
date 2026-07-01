# claude-notifier

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
