# 对话命令指定 Caption 位置 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户通过单次 `/caption N <链接>` 或 `/force N <链接>`，将本次命令每个相册分组的 caption 放在指定图片下。

**Architecture:** 在 `Uploader` 增加不持久化的单次发送选项，旧 `Send` 保持第一张的默认行为。Bot 在现有消息入口解析可选位置，只把整数选项传给本次上传；位置在每个分组内独立钳制，不进入下载结果或全局状态。

**Tech Stack:** Go 1.27、标准库 `strconv`/`strings`、现有 `goquery` 无新增依赖、`httptest.Server` 离线测试。

**Spec:** `docs/superpowers/specs/2026-11-22-caption-index-command-design.md`

## Global Constraints

- 所有 Go 测试与静态检查使用 `CGO_ENABLED=0`；测试不得访问真实站点或 Telegram。
- `/force <链接>`、普通链接及 CLI `upload` 的旧行为保持 caption 在第一项。
- `N<=0` 钳制为 `1`；`N>len(group)` 钳制为本组大小；每个分组复用同一个 `N`。
- 不保存聊天偏好，不新增配置文件字段，不把 caption 独立成文字消息。
- 每个任务单独提交，提交信息末尾使用 `Co-Authored-By: Kimi Code <noreply@moonshot.ai>`。

## Review Focus

- 旧 `/force` 与普通链接：不得改变强制下载和 caption 默认位置，由既有测试保证。
- 一次消息包含多个链接：相同位置传递到每个链接的上传，不改变各链接独立下载。
- 多组及不足指定位置：每组独立钳制，由任务 1 的 10+2 分组测试覆盖。
- 图片处理失败逐项重发：caption 仍落在原指定的同一项，由任务 1 的回退测试覆盖。
- 非法 `/caption`：不得下载或上传，由任务 2 的命令测试覆盖。

---

### Task 1: 单次上传的 Caption 位置选项

**Files:**
- Modify: `internal/telegram/uploader.go`
- Test: `internal/telegram/uploader_test.go`
- Test: `internal/telegram/cli_upload_test.go`（仅保留原有用例验证兼容性，通常无需修改）

**Interfaces:**
- Produces: `type SendOptions struct { CaptionIndex int }`。
- Produces: `func (u *Uploader) SendWithOptions(ctx context.Context, chatID string, post result.Post, opts SendOptions) (sent int, skipped []string, err error)`。
- Preserves: `Send` 的原签名和返回值，作为 `SendWithOptions(..., SendOptions{})` 的默认包装。
- Internal: `captionPosition(groupSize, requested int) int` 返回 0 起始下标；`groupSize<=0` 返回 `0`，其余按全局约束钳制。

- [ ] **Step 1: 写失败测试**

新增 `TestSendWithOptionsCaptionPosition`，无 caption 选项现有用例保持不变。用两张 JPEG 分别验证 `CaptionIndex` 为 `2`、`0`、`-3`、`99` 时，caption 只出现在第 2、1、1、2 项。新增 12 张图片的 `CaptionIndex:5` 用例，断言 10 项组的第 5 项、2 项组的第 2 项各有且仅有一个 caption。测试先引用尚未实现的 `SendWithOptions`。

- [ ] **Step 2: 观看 RED**

Run: `CGO_ENABLED=0 go test ./internal/telegram -run '^TestSendWithOptionsCaptionPosition' -v`
Expected: 编译失败，提示新类型或方法未定义。

- [ ] **Step 3: 最小实现上传选项**

保留旧 `Send` 为兼容包装；把现有上传主体移入 `SendWithOptions`。给分组发送增加位置参数，以 `captionPosition` 分别为 `sendMediaGroup` 和图片处理失败后的逐项重发选择单项 caption。不得给同一组其他项重复写 caption，也不得修改文件计数、顺序、拆组或 document 路径。

- [ ] **Step 4: 扩展图片处理失败用例并观看 RED→GREEN**

将现有两项图片回退用例改为 `SendWithOptions(..., SendOptions{CaptionIndex:2})` 的独立新用例：第一项单发送不带 caption，第二项处理失败后作为 document 仍携带 caption。先确认改造前行为不满足，再使其通过。运行 `CGO_ENABLED=0 go test ./internal/telegram -run 'Caption|FallsBack'`。

- [ ] **Step 5: 运行整个 Telegram 包并提交**

Run: `CGO_ENABLED=0 go test ./internal/telegram`。所有新增和既有测试（包括 CLI upload 默认位置）必须通过。随后提交本任务，message 为 `feat: 支持指定单次上传 caption 位置`，并附规定的 Co-Authored-By 行。

### Task 2: Bot 单次命令接入

**Files:**
- Modify: `internal/telegram/bot.go`（消息入口、帮助和上传调用）
- Test: `internal/telegram/bot_test.go`
- Modify: `docs/superpowers/specs/2026-10-28-go-rewrite-design.md`
- Add: `docs/superpowers/specs/2026-11-22-caption-index-command-design.md`（已批准设计）
- Add: `docs/superpowers/plans/2026-11-22-caption-index-command.md`（本计划）

**Interfaces:**
- Consumes: Task 1 的 `SendOptions` 与 `SendWithOptions`。
- Produces: 包内命令解析 helper，返回剩余分享文案、`force`、`captionIndex`、有效性；默认 `captionIndex=1`。具体命名可由实现者选择，但不能改变 `Dispatch` 签名或存全局偏好。

- [ ] **Step 1: 写失败命令测试**

新增离线 bot 测试，分别用两张图片验证 `/caption 2 <URL>` 正常分发且第二项带 caption，`/force 2 <URL>` 强制分发且第二项带 caption。另测 `/caption 2 <URL-A> <URL-B>` 两个链接均有此行为。非法命令 `/caption`、`/caption x <URL>`、`/caption 2` 均返回用法提示且 `Dispatch` 调用数为 0。`/forceable` 和旧 `/force` 测试保持原预期。

- [ ] **Step 2: 观看 RED**

Run: `CGO_ENABLED=0 go test ./internal/telegram -run 'CaptionCommand|ForceCaption' -v`
Expected: `/caption` 未按新命令处理、位置仍默认为第一项，相关测试失败；失败不得来自 fixture 或依赖缺失。

- [ ] **Step 3: 实现受限命令解析**

只在现有 bot 消息入口识别独立的 `/caption` 和 `/force` 首字段。`/caption` 必须有合法第二个整数字段及后续链接；负数按 Task 1 的钳制处理。`/force` 后首个字段能解析为整数时消耗为位置，否则保持旧分享文案处理。去除命令 token 后交给现有 `ExtractURLs`，每个链接复用同一位置。非法 `/caption` 只返回简短用法提示。

- [ ] **Step 4: 传入本次上传并更新帮助与文档**

Bot 将默认或解析得到的位置传给 `SendWithOptions`，不改变 `Dispatch`、`Result.Post`、CLI upload 或其他聊天状态。帮助增加 `/caption N <链接>` 和 `/force [N] <链接>`；设计文档说明单次生效、各组重复应用和边界钳制。

- [ ] **Step 5: 全仓验证并提交**

Run: `gofmt -w internal/telegram/bot.go internal/telegram/bot_test.go`、`CGO_ENABLED=0 go test ./...`、`CGO_ENABLED=0 go vet ./...`、`goreleaser check`、`git diff --check`。全部成功后将任务文件与上面列出的已批准设计、本计划一并提交，message 为 `feat: 支持对话命令指定 caption 位置`，并附规定的 Co-Authored-By 行。
