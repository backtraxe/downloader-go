# 通用页面 DOM 作者提取 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 通用后端能够从酷安 `.username-item` 结构提取作者，同时保留现有作者来源的优先级和回退行为。

**Architecture:** 在 `internal/backend/generic` 增加独立的受限 DOM 提取函数，并在 `Download` 的作者来源链中置于 `infotpc` 之后、标题推断之前。只读取指定节点的首个段落，不执行页面脚本。

**Tech Stack:** Go 1.27、goquery、`httptest.Server`、Go 标准测试工具。

**Spec:** `docs/superpowers/specs/2026-11-16-generic-dom-author-design.md`

## Global Constraints

- 所有 Go 构建与测试使用 `CGO_ENABLED=0`；单元测试不得访问真实站点。
- 作者来源优先级固定为：`infotpc` → 首个 `.username-item` 的首个 `p` → 标题规则 → 空字符串。
- 不拼接第二个 `p`，不修改媒体筛选、标题清理、下载或输出目录逻辑。
- 不新增依赖，不执行页面脚本；未经用户明确要求不提交代码。

## Review Focus

- `nil` 文档：不得 panic，由单元测试断言返回空串。
- 缺失 `.username-item` 或首段仅含空白：返回空串并让主链路回退标题，由单元测试与现有回退测试覆盖。
- 第二个段落含时间和设备名：只读取首段，由酷安结构测试断言作者精确等于 `忘记密码是啥了`。
- 同时存在 `infotpc` 和 DOM 作者：必须保留 `infotpc` 优先，由下载链路优先级测试覆盖。
- DOM 中没有作者：下载仍按原逻辑完成而非失败，由现有通用下载测试覆盖。

---

### Task 1: 受限 DOM 作者提取与下载链路接入

**Files:**
- Modify: `internal/backend/generic/extract.go`
- Modify: `internal/backend/generic/generic.go:87-90`
- Test: `internal/backend/generic/generic_test.go`
- Modify: `docs/superpowers/specs/2026-10-28-go-rewrite-design.md`

**Interfaces:**
- Consumes: `goquery.Document`、`ExtractAuthor(title, site string) string`、现有 `extractEmbeddedAuthor(doc *goquery.Document) string`。
- Produces: `extractDOMAuthor(doc *goquery.Document) string`，由 `Downloader.Download` 在 `infotpc` 为空时调用。

- [ ] **Step 1: 编写失败的 DOM 提取单元测试**

新增 `TestExtractDOMAuthor`，使用样例的 `.username-item` 两个段落，断言返回 `忘记密码是啥了` 且不含时间和 `vivo X200 Ultra`。表驱动补充 `nil`、无 `.username-item`、首段空白三种情况，均断言空串。测试先引用尚未实现的 `extractDOMAuthor`。

- [ ] **Step 2: 运行测试并确认因函数缺失而失败**

Run: `CGO_ENABLED=0 go test ./internal/backend/generic -run '^TestExtractDOMAuthor$' -v`
Expected: 编译失败，明确提示 `extractDOMAuthor` 未定义。

- [ ] **Step 3: 实现最小 DOM 提取函数**

在 `internal/backend/generic/extract.go` 实现签名 `func extractDOMAuthor(doc *goquery.Document) string`。`nil` 返回空串；否则选择第一个 `.username-item`，再选择其第一个 `p`，对 `Text()` 做 `strings.TrimSpace`。不要遍历或拼接其他段落。

- [ ] **Step 4: 验证 DOM 单元测试通过**

Run: `CGO_ENABLED=0 go test ./internal/backend/generic -run '^TestExtractDOMAuthor$' -v`
Expected: PASS。

- [ ] **Step 5: 编写下载链路和优先级失败测试**

新增 `TestDownloadUsesDOMAuthor`，通过 `httptest.Server` 返回无 `infotpc` 的酷安最小页面及一张有效测试图片，断言下载成功、`Post.Author == "忘记密码是啥了"`。新增 `TestDownloadPrefersEmbeddedAuthorOverDOM`，页面同时含 `infotpc`（作者 `内联作者`）和 DOM 作者，断言最终作者为 `内联作者`。沿用现有通用下载测试的数据目录和客户端注入方式，不访问真实站点。

- [ ] **Step 6: 运行测试并确认 DOM 链路失败**

Run: `CGO_ENABLED=0 go test ./internal/backend/generic -run '^(TestDownloadUsesDOMAuthor|TestDownloadPrefersEmbeddedAuthorOverDOM)$' -v`
Expected: `TestDownloadUsesDOMAuthor` 因作者为空而失败；优先级测试可独立通过。

- [ ] **Step 7: 把 DOM 提取接入作者回退链**

在 `generic.go` 保持 `extractEmbeddedAuthor` 优先；仅当结果为空时调用 `extractDOMAuthor`；仍为空时才调用 `ExtractAuthor(rawTitle, site)`。不要改动相邻标题和媒体逻辑。

- [ ] **Step 8: 运行通用后端完整测试**

Run: `CGO_ENABLED=0 go test ./internal/backend/generic`
Expected: 新增测试与所有现有测试全部 PASS。

- [ ] **Step 9: 更新行为文档并做最终验证**

在 Go 重写设计文档的作者提取说明中加入 `infotpc → .username-item 首段 → 标题` 的优先级。Run: `gofmt -w internal/backend/generic/extract.go internal/backend/generic/generic.go internal/backend/generic/generic_test.go`，再运行 `CGO_ENABLED=0 go test ./...`、`CGO_ENABLED=0 go vet ./...` 和 `git diff --check`，均须成功。
