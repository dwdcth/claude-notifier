# Merge claude-remote-approver into claude-notifier

## Goal

将 claude-remote-approver 的远程审批功能用 Go 重写，集成到 claude-notifier 中，使其同时支持 Notification（单向通知）和 PermissionRequest（双向审批）两种 Claude Code hook 事件。

## What I already know

### claude-notifier (Go)
- 单二进制，TOML 配置，编译时插件注册
- 处理 `hooks.Notification`：从 stdin 读 JSON → 并发分发到多个 notifier
- 核心接口：`Notifier.Send(ctx, Notification) error`
- 已有 ntfy 插件（HTTP POST，支持模板、认证、action headers）
- 已有 terminal-notifier 插件（macOS 桌面通知）
- CLI: `urfave/cli/v2`，子命令 init / test
- "永不失败"设计：所有错误路径 return nil

### claude-remote-approver (Node.js)
- 处理 `hooks.PermissionRequest`：stdin JSON → ntfy 推送 → SSE 等待响应 → stdout 决策
- 三种决策：allow / deny / ask（超时回退）
- Always Approve：当 Claude 提供 `permission_suggestions` 时，增加第三个按钮
- AskUserQuestion：将问题选项作为通知按钮发送
- 配置：随机 topic（`cra-` 前缀 + 32 hex）、超时（默认 120s）、Basic Auth
- CLI 子命令：setup / test / status / hook / enable / disable / uninstall
- ntfy SSE 通信：POST 发通知 → GET `<topic>-response/json` SSE 等待

## Assumptions (temporary)

- 审批功能仅通过 ntfy 实现（SSE 双向通信是 ntfy 特有能力）
- 配置统一使用现有 TOML 格式，扩展 `[approver]` section
- 审批的超时默认比通知长（120s vs 10s），因为需要等待用户手机响应
- AskUserQuestion 支持作为 MVP 的一部分

## Open Questions

(全部已确认 — MVP 包含完整功能)

## Requirements

1. **PermissionRequest hook 处理**
   - 新增 CLI 子命令 `hook`，从 stdin 读取 PermissionRequest JSON
   - 解析 `tool_name`、`tool_input`、`permission_suggestions` 字段
   - 输出决策 JSON 到 stdout（allow/deny/ask + updatedPermissions）

2. **ntfy SSE 双向通信**
   - 发送带 action 按钮的 ntfy 通知（Approve / Deny / Always Approve）
   - SSE 订阅 `<topic>-response/json`，按 requestId 过滤匹配响应
   - 支持重试（最多 3 次，线性退避）

3. **决策逻辑**
   - Approve → `{decision: {behavior: "allow"}}`
   - Deny → `{decision: {behavior: "deny"}}`
   - Always Approve → `{decision: {behavior: "allow"}, updatedPermissions: [...]}`
   - 超时/无配置 → 回退 ask（CLI 终端提示）

4. **AskUserQuestion 支持**
   - 将问题选项作为 ntfy action 按钮发送
   - 超过 3 个选项时分批发送多条通知

5. **配置管理**
   - TOML 扩展 `[approver]` section：topic、server、timeout、认证
   - topic 自动生成（crypto/rand，128-bit）
   - 配置文件权限 0600

6. **新 CLI 子命令**
   - `setup`：生成 topic + 保存配置 + 注册 hook 到 settings.json
   - `status`：显示当前审批配置
   - `enable` / `disable`：启用/禁用 hook（不改变配置）
   - `uninstall`：注销 hook + 删除审批配置

7. **向后兼容**
   - 现有 Notification hook 功能不受影响
   - 现有 TOML 配置格式不变，只扩展

## Acceptance Criteria

- [ ] `claude-notifier hook` 能正确处理 PermissionRequest stdin JSON
- [ ] Approve/Deny 通过 ntfy 按钮正确传递到 stdout
- [ ] SSE 超时正确回退到 ask
- [ ] AskUserQuestion 选项作为按钮发送
- [ ] `claude-notifier setup` 自动注册 hook 到 ~/.claude/settings.json
- [ ] 现有 Notification hook 功能不受影响
- [ ] 所有新增代码有单元测试
- [ ] `go build` 编译通过

## Definition of Done

- Tests added/updated (unit for all new modules)
- `go vet` / `go build` clean
- 配置文件示例更新
- README 更新说明新功能

## Out of Scope

- QR 码显示（remote-approver 有 qrcode-terminal，Go 版暂不加）
- 自托管 ntfy 服务器的 TLS 证书验证配置
- notify-send / Slack / Discord 等其他通知渠道的审批支持

## Technical Notes

### 现有架构
- `internal/notifier/` — Notifier 接口 + Registry
- `internal/dispatch/` — 并发分发
- `internal/config/` — TOML 加载
- `internal/cli/` — CLI 入口
- `internal/tmpl/` — Go 模板
- `plugins/ntfy/` — ntfy 发送插件
- `plugins/terminalnotifier/` — macOS 通知

### 新增模块（规划）
- `internal/approver/` — 审批核心逻辑（解析输入、构建通知、处理决策）
- `internal/ntfyclient/` — ntfy 双向通信（POST + SSE）
- `internal/settings/` — 读写 ~/.claude/settings.json
- `plugins/ntfy/` 扩展 SSE 能力

### 关键协议
PermissionRequest 输入:
```json
{
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": {"command": "npm test"},
  "permission_suggestions": [{"type": "toolAlwaysAllow", "tool": "Bash"}]
}
```

PermissionRequest 输出:
```json
{
  "hookSpecificOutput": {
    "hookEventName": "PermissionRequest",
    "decision": {"behavior": "allow"},
    "updatedPermissions": [{"type": "toolAlwaysAllow", "tool": "Bash"}]
  }
}
```

### References
- claude-remote-approver 源码：https://github.com/yuuichieguchi/claude-remote-approver
- claude-notifier 源码：https://github.com/felipeelias/claude-notifier
- ntfy actions 文档：https://docs.ntfy.sh/publish/#action-buttons
- ntfy SSE 文档：https://docs.ntfy.sh/subscribe/api/#subscribe-as-json-stream
