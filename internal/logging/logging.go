// Package logging 提供带请求作用域的 slog logger。
//
// 每个链接处理期间使用一个随机短 ID（形如 [rK3f2aB9]），日志消息前缀统一带上
// 该 ID；grep 同一个 ID 可串联单个链接从下载到上传的完整轨迹。作用域通过
// context 传递，嵌套开启时复用已有 ID，不会重新生成。
package logging

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
)

// idAlphabet 是请求 ID 的字符集：大小写字母与数字。
const idAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// idLength 是 r 前缀后的随机位数，整个 ID 共 8 位。
const idLength = 7

var (
	idMu   sync.Mutex
	usedID = make(map[string]struct{})
)

// requestKey 是 context 中保存请求 ID 的私有键。
type requestKey struct{}

// New 返回写入 w 的 logger，低于 level 的日志被丢弃。
// 输出为简短单行："YYYYMMDD HHMMSS+08 LEVEL [rXXXXXXX] message key=value"；
// 消息在携带请求 ID 的 context 中输出时自动带上前缀，便于用 grep 串联一次下载。
// 时区沿用全局设置，默认 Asia/Shanghai，可用 DOWNLOADER_TZ 覆盖。
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(requestHandler{Handler: newShortHandler(w, level)})
}

// shortHandler 是便于在终端阅读的 slog 文本 handler。
// 字段保留 key=value，含空白或引号的值会转义，避免日志变成难解析的长句。
type shortHandler struct {
	w      io.Writer
	level  slog.Level
	attrs  []slog.Attr
	groups []string
	mu     *sync.Mutex
}

func newShortHandler(w io.Writer, level slog.Level) shortHandler {
	return shortHandler{w: w, level: level, mu: &sync.Mutex{}}
}

func (h shortHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h shortHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	if !record.Time.IsZero() {
		line.WriteString(record.Time.Format("20060102 150405-07"))
		line.WriteByte(' ')
	}
	line.WriteString(record.Level.String())
	line.WriteByte(' ')
	line.WriteString(record.Message)
	for _, attr := range h.attrs {
		writeAttr(&line, attr, h.groups)
	}
	record.Attrs(func(attr slog.Attr) bool {
		writeAttr(&line, attr, h.groups)
		return true
	})
	line.WriteByte('\n')

	// WithAttrs/WithGroup 返回的副本共享锁，确保并发下载不会交叉写行。
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line.String())
	return err
}

func (h shortHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	h.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return h
}

func (h shortHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h.groups = append(append([]string(nil), h.groups...), name)
	return h
}

func writeAttr(line *strings.Builder, attr slog.Attr, groups []string) {
	if attr.Equal(slog.Attr{}) {
		return
	}
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			groups = append(append([]string(nil), groups...), attr.Key)
		}
		for _, nested := range value.Group() {
			writeAttr(line, nested, groups)
		}
		return
	}

	line.WriteByte(' ')
	for _, group := range groups {
		line.WriteString(group)
		line.WriteByte('.')
	}
	line.WriteString(attr.Key)
	line.WriteByte('=')
	text := value.String()
	if text == "" || strings.ContainsAny(text, " \t\r\n\"") {
		text = strconv.Quote(text)
	}
	line.WriteString(text)
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

// NewID 生成进程内唯一的 8 位请求 ID：r + 7 位 [A-Za-z0-9]。
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
