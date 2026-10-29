package tgapi

import "encoding/json"

// Message 是 Bot API 返回的消息对象中本客户端用到的字段。
type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      User   `json:"from"`
	Text      string `json:"text"`
	Caption   string `json:"caption"`
}

// Chat 是消息所属聊天。ID 用 json.Number 保存，既可直接表示数字 ID，
// 也可由调用方以字符串形式持有 @channel 用户名；数字 ID 经 String()
// 转换后可直接用于 API 的 chat_id 参数。
type Chat struct {
	ID   json.Number `json:"id"`
	Type string      `json:"type"`
}

// User 是消息发送者。
type User struct {
	ID int64 `json:"id"`
}

// Update 是 getUpdates 返回的一条更新；私聊/群聊消息在 Message，
// 频道帖子在 ChannelPost。
type Update struct {
	UpdateID    int64    `json:"update_id"`
	Message     *Message `json:"message"`
	ChannelPost *Message `json:"channel_post"`
}

// Media 描述 sendMediaGroup 中的一个媒体项。Kind 取 photo、video 或
// document；Path 是本地文件路径；Caption 仅第一项生效。
type Media struct {
	Kind    string
	Path    string
	Caption string
}
