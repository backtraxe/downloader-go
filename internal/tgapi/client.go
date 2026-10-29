// Package tgapi 是 Telegram Bot API 的最小客户端，只依赖标准库。
//
// 覆盖本项目的使用场景：长轮询拉取更新、发送/删除文字消息、上传媒体
// 与媒体组。所有方法都接受 context，调用方据此控制超时（getUpdates 的
// 长轮询尤其需要比普通请求更长的超时）。
package tgapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DefaultBaseURL 是官方 Bot API 地址。
const DefaultBaseURL = "https://api.telegram.org"

const (
	// maxErrorBodyTail 限制错误信息中回显的响应体长度，避免日志被大响应刷屏。
	maxErrorBodyTail = 512
	// maxResponseBody 限制读取的响应体大小；超出按截断处理并报告。
	maxResponseBody = 1 << 20
	// redactedToken 替换错误信息中的 bot token，避免凭据随错误进入日志。
	redactedToken = "<redacted>"
)

// Client 是 Bot API 客户端。Zero value 不可用，请用 New 构造；
// 测试可覆盖 BaseURL 与 HTTP。
type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// New 返回使用官方 API 地址与 http.DefaultClient 的客户端。
func New(token string) *Client {
	return &Client{
		Token:   token,
		BaseURL: DefaultBaseURL,
		HTTP:    http.DefaultClient,
	}
}

// Call 以 application/x-www-form-urlencoded 调用 method，并返回 result
// 的原始 JSON。params 可为 nil。
func (c *Client) Call(ctx context.Context, method string, params url.Values) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("telegram %s: %w", method, c.redactTransportError(err))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, method)
}

// CallMultipart 以 multipart/form-data 调用 method。fields 作为普通文本
// 字段写入，files 的 key 为表单字段名、value 为本地文件路径，文件名取
// 路径的 basename。返回 result 的原始 JSON。
//
// 请求体写到临时文件而不是内存：50MB 视频或 10 项相册在 Termux 上整包
// 缓存会 OOM。写入完成后回到文件头，显式设置 ContentLength 再发送，
// 请求结束（无论成败）删除临时文件。
func (c *Client) CallMultipart(ctx context.Context, method string, fields map[string]string, files map[string]string) (json.RawMessage, error) {
	tmp, err := os.CreateTemp("", "tgapi-multipart-*")
	if err != nil {
		return nil, fmt.Errorf("telegram %s: create temp file: %w", method, err)
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	mw := multipart.NewWriter(tmp)
	for name, value := range fields {
		if err := mw.WriteField(name, value); err != nil {
			return nil, fmt.Errorf("telegram %s: write field %s: %w", method, name, err)
		}
	}
	// 固定文件字段顺序，便于复现请求（与 API 语义无关）。
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := files[name]
		if err := writeFilePart(mw, name, path); err != nil {
			return nil, fmt.Errorf("telegram %s: %w", method, err)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("telegram %s: close multipart: %w", method, err)
	}
	info, err := tmp.Stat()
	if err != nil {
		return nil, fmt.Errorf("telegram %s: stat temp file: %w", method, err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("telegram %s: rewind temp file: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), tmp)
	if err != nil {
		return nil, fmt.Errorf("telegram %s: %w", method, c.redactTransportError(err))
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return c.do(req, method)
}

// writeFilePart 把本地文件作为表单文件字段写入。
func writeFilePart(mw *multipart.Writer, field, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	part, err := mw.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("create file part %s: %w", field, err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return fmt.Errorf("copy %s: %w", path, err)
	}
	return nil
}

// GetUpdates 长轮询拉取 offset 之后的更新。
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	params := url.Values{
		"offset":  {strconv.FormatInt(offset, 10)},
		"timeout": {strconv.Itoa(timeout)},
	}
	raw, err := c.Call(ctx, "getUpdates", params)
	if err != nil {
		return nil, err
	}
	var updates []Update
	if err := decodeResult("getUpdates", raw, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// SendMessage 发送文字消息。
func (c *Client) SendMessage(ctx context.Context, chatID, text string) (Message, error) {
	params := url.Values{
		"chat_id": {chatID},
		"text":    {text},
	}
	raw, err := c.Call(ctx, "sendMessage", params)
	var msg Message
	if err != nil {
		return msg, err
	}
	if err := decodeResult("sendMessage", raw, &msg); err != nil {
		return msg, err
	}
	return msg, nil
}

// DeleteMessage 删除指定消息。
func (c *Client) DeleteMessage(ctx context.Context, chatID string, messageID int64) error {
	params := url.Values{
		"chat_id":    {chatID},
		"message_id": {strconv.FormatInt(messageID, 10)},
	}
	_, err := c.Call(ctx, "deleteMessage", params)
	return err
}

// SendMediaGroup 以媒体组（相册）发送多个媒体。第一项的 Caption 作为
// 整组说明；空 Caption 不写入 media JSON。返回服务端创建的若干消息。
func (c *Client) SendMediaGroup(ctx context.Context, chatID string, media []Media) ([]Message, error) {
	type mediaItem struct {
		Type    string `json:"type"`
		Media   string `json:"media"`
		Caption string `json:"caption,omitempty"`
	}
	items := make([]mediaItem, 0, len(media))
	files := make(map[string]string, len(media))
	for i, m := range media {
		attach := "file" + strconv.Itoa(i)
		item := mediaItem{Type: m.Kind, Media: "attach://" + attach}
		if i == 0 {
			item.Caption = m.Caption
		}
		items = append(items, item)
		files[attach] = m.Path
	}
	mediaJSON, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("telegram sendMediaGroup: encode media: %w", err)
	}
	raw, err := c.CallMultipart(ctx, "sendMediaGroup", map[string]string{
		"chat_id": chatID,
		"media":   string(mediaJSON),
	}, files)
	if err != nil {
		return nil, err
	}
	var msgs []Message
	if err := decodeResult("sendMediaGroup", raw, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

// endpoint 拼出 Bot API 方法 URL。
func (c *Client) endpoint(method string) string {
	return strings.TrimRight(c.BaseURL, "/") + "/bot" + c.Token + "/" + method
}

// do 执行请求并按 Bot API 的统一响应结构解包 result。
func (c *Client) do(req *http.Request, method string) (json.RawMessage, error) {
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram %s: %w", method, c.redactTransportError(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("telegram %s: read response: %w", method, err)
	}
	truncated := len(body) > maxResponseBody
	if truncated {
		body = body[:maxResponseBody]
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := fmt.Sprintf("telegram %s: HTTP %s: %s", method, resp.Status, c.bodyTail(body))
		if truncated {
			msg += " (response truncated)"
		}
		return nil, errors.New(msg)
	}
	if truncated {
		return nil, fmt.Errorf("telegram %s: response truncated at %d bytes", method, maxResponseBody)
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("telegram %s: decode response: %w", method, err)
	}
	if !envelope.OK {
		desc := c.redact(envelope.Description)
		if desc == "" {
			desc = "request failed"
		}
		return nil, fmt.Errorf("telegram %s: %s", method, desc)
	}
	return envelope.Result, nil
}

// redact 把出现在 s 中的 bot token 替换为占位符。
func (c *Client) redact(s string) string {
	if c.Token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Token, redactedToken)
}

// redactTransportError 清除传输错误中的 bot token。net/http 返回的
// *url.Error 会带上完整请求 URL（其中含 token），这里复制一份并替换
// URL；其余包含 token 的错误退化为纯文本错误，宁可丢失错误链也不泄漏。
func (c *Client) redactTransportError(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		clone := *ue
		clone.URL = c.redact(ue.URL)
		if c.Token != "" && ue.Err != nil && strings.Contains(ue.Err.Error(), c.Token) {
			clone.Err = errors.New(c.redact(ue.Err.Error()))
		}
		return &clone
	}
	if c.Token != "" && strings.Contains(err.Error(), c.Token) {
		return errors.New(c.redact(err.Error()))
	}
	return err
}

// decodeResult 把 result 原始 JSON 解到 out；result 缺失时按零值处理。
func decodeResult(method string, raw json.RawMessage, out any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}
	return nil
}

// bodyTail 截取响应体尾部，供错误信息回显；先脱敏 token，再在 UTF-8
// rune 边界处截断，避免日志出现半个多字节字符。
func (c *Client) bodyTail(body []byte) string {
	s := c.redact(strings.TrimSpace(string(body)))
	if len(s) <= maxErrorBodyTail {
		return s
	}
	s = s[len(s)-maxErrorBodyTail:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return "..." + strings.ToValidUTF8(s, "�")
}
