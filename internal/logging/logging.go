// Package logging 提供带请求作用域的 slog logger。
//
// 每个链接处理期间使用一个随机短 ID（形如 [rk3f2]），日志消息前缀统一带上
// 该 ID；grep 同一个 ID 可串联单个链接从下载到上传的完整轨迹。作用域通过
// context 传递，嵌套开启时复用已有 ID，不会重新生成。
package logging

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
)

// idAlphabet 是请求 ID 的字符集：小写字母与数字。
const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// idLength 是随机部分长度；36^4 ≈ 168 万种取值。
const idLength = 4

var (
	idMu   sync.Mutex
	usedID = make(map[string]struct{})
)

// requestKey 是 context 中保存请求 ID 的私有键。
type requestKey struct{}

// New 返回写入 w 的 logger，低于 level 的日志被丢弃。
// 消息在携带请求 ID 的 context 中输出时自动加上 "[id] " 前缀。
func New(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(requestHandler{Handler: handler})
}

// WithRequest 开启一段请求日志作用域并返回其 ID。
// ctx 已带请求 ID 时直接复用，保证嵌套作用域共用同一个 ID。
func WithRequest(ctx context.Context) (context.Context, string) {
	if id := RequestID(ctx); id != "" {
		return ctx, id
	}
	id := NewID()
	return context.WithValue(ctx, requestKey{}, id), id
}

// RequestID 返回 ctx 中的请求 ID；没有作用域时返回空串。
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestKey{}).(string)
	return id
}

// NewID 生成进程内唯一的请求 ID：r + 4 位 [a-z0-9]。
func NewID() string {
	idMu.Lock()
	defer idMu.Unlock()
	for {
		buf := make([]byte, idLength)
		for i := range buf {
			buf[i] = idAlphabet[rand.IntN(len(idAlphabet))]
		}
		id := "r" + string(buf)
		if _, exists := usedID[id]; !exists {
			usedID[id] = struct{}{}
			return id
		}
	}
}

// requestHandler 把请求 ID 前缀注入日志消息。
type requestHandler struct {
	slog.Handler
}

// Handle 在 context 带请求 ID 时给消息加 "[id] " 前缀。
func (h requestHandler) Handle(ctx context.Context, record slog.Record) error {
	if id := RequestID(ctx); id != "" {
		record = record.Clone()
		record.Message = "[" + id + "] " + record.Message
	}
	return h.Handler.Handle(ctx, record)
}

// WithAttrs 保留前缀包装，避免内层 handler 直接返回丢失 ID 注入。
func (h requestHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return requestHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup 保留前缀包装，避免内层 handler 直接返回丢失 ID 注入。
func (h requestHandler) WithGroup(name string) slog.Handler {
	return requestHandler{Handler: h.Handler.WithGroup(name)}
}
