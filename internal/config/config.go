// Package config 加载仓库统一的 config.json。
//
// config.json 不只服务于 Telegram：任何需要用户级配置的模块都在该文件的
// 顶层键下读写自己的配置。当前只有 telegram 段：
//
//	bot_token / chat_id / allowed_users / process_message_ttl_seconds
//
// 向后兼容：config.json 缺失时读取旧版扁平结构的 telegram.json，内容等价于
// {"telegram": <其内容>}；两者都不存在时写入模板并返回 ErrNotConfigured，
// 由调用方打印指引后退出。文件路径由调用方传入，通常相对于数据目录。
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// ErrNotConfigured 表示 config.json 与旧版 telegram.json 都不存在。
// 此时 Load 已把模板写入 Path，调用方应提示用户填写后重试。
var ErrNotConfigured = errors.New("config not found")

// DefaultProcessMessageTTL 是过程提示消息的默认存活秒数。
const DefaultProcessMessageTTL = 300

// template 是缺失配置时写入的文件内容，与 Python 版保持一致。
const template = `{
  "telegram": {
    "bot_token": "在此填入 @BotFather 给的 token",
    "chat_id": 0,
    "allowed_users": [],
    "process_message_ttl_seconds": 300
  }
}
`

// ChatID 同时接受 JSON 数字（123）与字符串（"@channel"），
// 内部保留原始文本，String 原样返回。
type ChatID struct {
	text string
}

// String 返回 ChatID 的原始文本；零值为空串。
func (c ChatID) String() string {
	return c.text
}

// UnmarshalJSON 接受数字、字符串与 null，拒绝布尔等其它类型。
func (c *ChatID) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		c.text = ""
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return fmt.Errorf("chat_id 解析失败: %w", err)
		}
		c.text = s
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return fmt.Errorf("chat_id 必须是数字或字符串: %w", err)
	}
	c.text = n.String()
	return nil
}

// MarshalJSON 在文本可解析为整数时输出数字，否则输出带引号的字符串。
// 数字会规范化（如 "007" 输出 7），保证结果是合法 JSON。
func (c ChatID) MarshalJSON() ([]byte, error) {
	if n, err := strconv.ParseInt(c.text, 10, 64); err == nil {
		return []byte(strconv.FormatInt(n, 10)), nil
	}
	return json.Marshal(c.text)
}

// Telegram 是 config.json 的 telegram 段。
type Telegram struct {
	BotToken     string  `json:"bot_token"`
	ChatID       ChatID  `json:"chat_id"`
	AllowedUsers []int64 `json:"allowed_users"`
	// ProcessMessageTTLSeconds 为 nil 表示配置缺键，使用默认 300 秒；
	// 指向 0 表示显式关闭过程提示消息的自动删除。
	ProcessMessageTTLSeconds *int `json:"process_message_ttl_seconds"`
}

// TTL 返回过程提示消息的存活秒数：缺省 300，显式 0 返回 0。
func (t Telegram) TTL() int {
	if t.ProcessMessageTTLSeconds == nil {
		return DefaultProcessMessageTTL
	}
	return *t.ProcessMessageTTLSeconds
}

// Config 是 config.json 的顶层结构。
type Config struct {
	Telegram Telegram `json:"telegram"`
}

// LoadOptions 指定配置文件位置。
type LoadOptions struct {
	// Path 是 config.json 的路径。
	Path string
	// LegacyPath 是旧版 telegram.json 的路径，可为空。
	LegacyPath string
}

// Load 依次尝试 config.json、旧版 telegram.json。
// 两者都不存在时把模板写入 Path 并返回 ErrNotConfigured；
// 文件存在但解析失败时返回错误，不回退到旧版文件。
func Load(opts LoadOptions) (*Config, error) {
	if opts.Path != "" {
		data, err := os.ReadFile(opts.Path)
		switch {
		case err == nil:
			var cfg Config
			if err := json.Unmarshal(data, &cfg); err != nil {
				return nil, fmt.Errorf("解析配置 %s: %w", opts.Path, err)
			}
			return &cfg, nil
		case !errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("读取配置 %s: %w", opts.Path, err)
		}
	}

	if opts.LegacyPath != "" {
		data, err := os.ReadFile(opts.LegacyPath)
		switch {
		case err == nil:
			var tg Telegram
			if err := json.Unmarshal(data, &tg); err != nil {
				return nil, fmt.Errorf("解析旧版配置 %s: %w", opts.LegacyPath, err)
			}
			return &Config{Telegram: tg}, nil
		case !errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("读取旧版配置 %s: %w", opts.LegacyPath, err)
		}
	}

	if opts.Path == "" {
		return nil, ErrNotConfigured
	}
	if err := WriteTemplate(opts.Path); err != nil {
		return nil, err
	}
	return nil, ErrNotConfigured
}

// WriteTemplate 在 path 写入配置模板。文件已存在时不做任何修改，
// 绝不覆盖用户已有配置。
func WriteTemplate(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("写入配置模板 %s: %w", path, err)
	}
	if _, err := f.WriteString(template); err != nil {
		f.Close()
		return fmt.Errorf("写入配置模板 %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("写入配置模板 %s: %w", path, err)
	}
	return nil
}
