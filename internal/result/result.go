// Package result 定义所有下载后端与 Telegram 机器人共享的结果契约。
package result

import (
	"fmt"
	"strings"
)

// Status 是下载结果的归一化状态。
type Status string

const (
	// Downloaded 表示本次调用有新增或覆盖的文件。
	Downloaded Status = "downloaded"
	// Partial 表示部分成功、部分失败。
	Partial Status = "partial"
	// Skipped 表示目标已存在，本次调用没有写入文件。
	Skipped Status = "skipped"
	// Failed 表示请求、解析或落盘失败。
	Failed Status = "failed"
	// NoMedia 表示页面没有可下载的媒体，或媒体全部被过滤。
	NoMedia Status = "no_media"
)

// Post 描述一次下载中单个作品的结果。
type Post struct {
	Site   string
	Author string
	Title  string
	Status Status // 可选：播放列表逐 Post 判定；单 Post 时与顶层一致
	Error  string // 可选：该 Post 的失败原因
	// Files 仅包含本次调用写入或覆盖的文件；已存在而跳过时不填。
	Files []string
}

// Result 是一次后端调用的顶层结果，可能包含多个 Post。
type Result struct {
	Status  Status
	Message string
	Posts   []Post
}

// OK 表示结果是否可继续流程：下载成功、部分成功与已存在都算成功，
// 失败与无可下载内容不算。
func (r Result) OK() bool {
	switch r.Status {
	case Downloaded, Partial, Skipped:
		return true
	default:
		return false
	}
}

// Counts 汇总一次下载的计数，供后端构造 Result。
type Counts struct {
	Downloaded, Skipped, Failed, Filtered int
}

// FromCounts 按 新增 > 失败 > 已存在 > 被过滤 的优先级判定状态。
// 有新增也有失败时降为 Partial；消息中带上各项数量。
func FromCounts(c Counts) Result {
	status := NoMedia
	switch {
	case c.Downloaded > 0 && c.Failed > 0:
		status = Partial
	case c.Downloaded > 0:
		status = Downloaded
	case c.Failed > 0:
		status = Failed
	case c.Skipped > 0:
		status = Skipped
	}
	return Result{Status: status, Message: countsMessage(c)}
}

// countsMessage 生成简短中文计数摘要，顺序与状态判定优先级一致。
func countsMessage(c Counts) string {
	parts := make([]string, 0, 4)
	if c.Downloaded > 0 {
		parts = append(parts, fmt.Sprintf("新增 %d 项", c.Downloaded))
	}
	if c.Failed > 0 {
		parts = append(parts, fmt.Sprintf("失败 %d 项", c.Failed))
	}
	if c.Skipped > 0 {
		parts = append(parts, fmt.Sprintf("已存在 %d 项", c.Skipped))
	}
	if c.Filtered > 0 {
		parts = append(parts, fmt.Sprintf("过滤 %d 项", c.Filtered))
	}
	if len(parts) == 0 {
		return "未发现可下载内容"
	}
	return strings.Join(parts, "，")
}
