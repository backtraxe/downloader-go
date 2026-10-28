package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/backtraxe/downloader-go/internal/config"
	"github.com/backtraxe/downloader-go/internal/logging"
	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/tgapi"
	"github.com/backtraxe/downloader-go/internal/urlutil"
)

const (
	// pollTimeoutSeconds 是长轮询 getUpdates 的 timeout 参数。
	pollTimeoutSeconds = 25
	// startupTimeoutSeconds 用于启动时拉取积压消息，不等待新消息。
	startupTimeoutSeconds = 0
	// startupTimeout 限制启动时拉取积压消息的时长。
	startupTimeout = 15 * time.Second
	// pollTimeout 限制单次长轮询的时长；比 timeout 参数留出网络余量。
	pollTimeout = 40 * time.Second
	// retryDelay 是 getUpdates 失败后的重试间隔。
	retryDelay = 5 * time.Second
	// requestTimeout 限制单次前台 Telegram 调用（发消息、删除）。
	requestTimeout = 60 * time.Second

	// helpText 是 /start 与 /help 的回复。
	helpText = "发链接即可：小红书 / 抖音图文或视频 / bilibili / youtube / instagram / x / 任意网页。产物会以相册或视频回传，caption 带 #作者 标签方便搜索。"
	// noURLText 是消息中没有链接时的回复。
	noURLText = "没识别到链接，直接发链接（可带分享文案）即可。"
	// skippedText 是内容此前已下载过时的回复。
	skippedText = "没有新产物——该内容此前已下载过"
	// noNewFilesText 是下载状态成功但没有产物时的回复。
	noNewFilesText = "没有新产物——可能此前已下载过，或产物被过滤。"
)

// Bot 是长轮询机器人：收到链接后下载并把产物回传到同一聊天。
//
// Config 提供 token、白名单与过程消息 TTL；Dispatch 与 Uploader 分别
// 负责下载和上传，测试可替换为替身。AfterFunc 默认 time.AfterFunc，
// 测试可注入以观察删除调度。
type Bot struct {
	API       *tgapi.Client
	Config    config.Telegram
	DataDir   string
	Dispatch  func(ctx context.Context, url string, force bool) result.Result
	Uploader  *Uploader
	AfterFunc func(time.Duration, func()) *time.Timer
}

// Run 启动长轮询循环，直到 ctx 被取消。
//
// 启动时先拉取一次积压更新并把 offset 推进到最新，避免重启后重复下载
// 历史链接；之后的 getUpdates 失败会等待 5 秒再重试。ctx 取消时返回 nil。
func (b *Bot) Run(ctx context.Context) error {
	offset, err := b.initialOffset(ctx)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	for {
		// 每轮长轮询单独限时：半开连接不会让循环永久卡住，超时后走 5s 重试。
		pollCtx, cancel := context.WithTimeout(ctx, pollTimeout)
		updates, err := b.API.GetUpdates(pollCtx, offset, pollTimeoutSeconds)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Warn("getUpdates 失败，稍后重试", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(retryDelay):
			}
			continue
		}
		for _, update := range updates {
			offset = update.UpdateID + 1
			msg := update.Message
			if msg == nil {
				msg = update.ChannelPost
			}
			if msg == nil {
				continue
			}
			if err := b.HandleMessage(ctx, *msg); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				slog.Warn("处理消息失败", "error", err)
			}
		}
	}
}

// initialOffset 拉取一次积压更新并返回下一个 offset。
func (b *Bot) initialOffset(ctx context.Context) (int64, error) {
	if b.API == nil {
		return 0, errors.New("telegram bot: nil API client")
	}
	startCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	updates, err := b.API.GetUpdates(startCtx, 0, startupTimeoutSeconds)
	if err != nil {
		return 0, err
	}
	var offset int64
	for _, update := range updates {
		if next := update.UpdateID + 1; next > offset {
			offset = next
		}
	}
	return offset, nil
}

// HandleMessage 处理一条私聊/群聊消息或频道帖子。
func (b *Bot) HandleMessage(ctx context.Context, msg tgapi.Message) error {
	chatID := msg.Chat.ID.String()
	text := strings.TrimSpace(msg.Text)

	if strings.HasPrefix(text, "/start") || strings.HasPrefix(text, "/help") {
		return b.reply(ctx, chatID, helpText)
	}
	// 频道里只有管理员能发帖，且匿名管理员没有 from，无法匹配白名单。
	if msg.Chat.Type != "channel" && !b.allowed(msg.From.ID) {
		return b.reply(ctx, chatID, fmt.Sprintf("未授权用户。把你的用户 id 发给维护者加入白名单：%d", msg.From.ID))
	}
	if text == "" {
		return nil
	}

	urls := urlutil.ExtractURLs(text)
	if len(urls) == 0 {
		return b.reply(ctx, chatID, noURLText)
	}

	for _, link := range urls {
		// 单条链接 = 一个请求：下载与上传日志共享同一 [rXXXX] ID。
		reqCtx, _ := logging.WithRequest(ctx)
		if err := b.reply(reqCtx, chatID, "开始下载："+link); err != nil {
			return err
		}

		res := b.Dispatch(reqCtx, link, false)
		switch res.Status {
		case result.Failed:
			detail := res.Message
			if detail == "" {
				detail = "未知原因"
			}
			if err := b.reply(reqCtx, chatID, "下载失败："+detail); err != nil {
				return err
			}
			continue
		case result.NoMedia:
			message := "没有找到可下载的媒体。"
			if res.Message != "" {
				message = "没有找到可下载的媒体：" + res.Message
			}
			if err := b.reply(reqCtx, chatID, message); err != nil {
				return err
			}
			continue
		case result.Skipped:
			if err := b.reply(reqCtx, chatID, skippedText); err != nil {
				return err
			}
			continue
		}

		if len(res.Posts) == 0 {
			if err := b.reply(reqCtx, chatID, noNewFilesText); err != nil {
				return err
			}
			continue
		}

		sent, skipped := 0, 0
		for _, post := range res.Posts {
			n, skippedFiles, err := b.Uploader.Send(reqCtx, chatID, post)
			if err != nil {
				if replyErr := b.reply(reqCtx, chatID, "上传失败："+err.Error()); replyErr != nil {
					return replyErr
				}
				continue
			}
			sent += n
			skipped += len(skippedFiles)
		}

		completion := fmt.Sprintf("完成：%d 项已回传", sent)
		if skipped > 0 {
			completion += fmt.Sprintf("，%d 项超限跳过", skipped)
		}
		if (res.Status == result.Partial || res.Status == result.Failed) && res.Message != "" {
			completion += "（" + res.Message + "）"
		}
		if err := b.reply(reqCtx, chatID, completion); err != nil {
			return err
		}
	}
	return nil
}

// reply 发送文字消息；TTL > 0 且拿到 message_id 时调度删除。
// 媒体消息不经过这里，因此不会被自动删除。
func (b *Bot) reply(ctx context.Context, chatID, text string) error {
	if b.API == nil {
		return errors.New("telegram bot: nil API client")
	}
	sendCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	msg, err := b.API.SendMessage(sendCtx, chatID, text)
	cancel()
	if err != nil {
		return err
	}
	ttl := b.Config.TTL()
	if ttl <= 0 || msg.MessageID == 0 {
		return nil
	}
	after := b.AfterFunc
	if after == nil {
		after = time.AfterFunc
	}
	after(time.Duration(ttl)*time.Second, func() {
		deleteCtx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		// 删除失败只影响清理（频道内可能没有删除权限），不改变处理结果。
		if err := b.API.DeleteMessage(deleteCtx, chatID, msg.MessageID); err != nil {
			slog.Warn("删除过程消息失败", "error", err)
		}
	})
	return nil
}

// allowed 判断用户是否在白名单中；名单为空时拒绝所有用户。
func (b *Bot) allowed(userID int64) bool {
	for _, id := range b.Config.AllowedUsers {
		if id == userID {
			return true
		}
	}
	return false
}
