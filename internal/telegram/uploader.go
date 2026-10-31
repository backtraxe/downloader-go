package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/backtraxe/downloader-go/internal/result"
	"github.com/backtraxe/downloader-go/internal/tgapi"
)

// albumSize 是 sendMediaGroup 单组上限。
const albumSize = 10

// Uploader 把一次下载结果中的文件上传到 Telegram。
//
// FFmpeg/FFprobe 为空时使用 PATH 中的同名命令；Run 为空时直接执行外部
// 命令。WorkDir 是转码产物的落盘目录，为空时使用系统临时目录。
type Uploader struct {
	API     *tgapi.Client
	FFmpeg  string
	FFprobe string
	Run     Runner
	WorkDir string
}

// Send 上传 post.Files 并返回成功文件数与被跳过的本地路径。
//
// 照片先经 PreparePhoto：需转码且成功的进相册，失败的原样按 document
// 发送。视频超过 50MB 时尝试压到约 49MB，压缩失败或仍超限则加入
// skipped。其余文件逐个按 document 发送。相册每组的首项带
// BuildCaption 生成的说明。
func (u *Uploader) Send(ctx context.Context, chatID string, post result.Post) (sent int, skipped []string, err error) {
	skipped = []string{}
	if u.API == nil {
		return 0, skipped, errors.New("telegram: nil API client")
	}
	caption := BuildCaption(post.Site, post.Author, post.Title)

	var media []FileInfo
	var documents []string
	var generated []string
	defer func() {
		for _, path := range generated {
			_ = os.Remove(path)
		}
	}()

	for _, path := range post.Files {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return sent, skipped, fmt.Errorf("telegram: stat %s: %w", path, statErr)
		}
		switch Classify(path) {
		case Photo:
			prepared, asDocument, prepErr := u.PreparePhoto(ctx, path)
			if prepErr != nil {
				return sent, skipped, prepErr
			}
			if prepared != path {
				generated = append(generated, prepared)
			}
			if asDocument {
				if info.Size() > maxUploadBytes {
					skipped = append(skipped, path)
				} else {
					documents = append(documents, path)
				}
				continue
			}
			media = append(media, FileInfo{Path: prepared, Size: info.Size(), Kind: Photo})
		case Video:
			sendPath := path
			if info.Size() > maxUploadBytes {
				compressed, compressErr := u.CompressVideo(ctx, path, videoTargetBytes)
				if compressErr != nil {
					skipped = append(skipped, path)
					continue
				}
				generated = append(generated, compressed)
				compressedInfo, statErr := os.Stat(compressed)
				if statErr != nil || compressedInfo.Size() > maxUploadBytes {
					skipped = append(skipped, path)
					continue
				}
				sendPath = compressed
				info = compressedInfo
			}
			media = append(media, FileInfo{Path: sendPath, Size: info.Size(), Kind: Video})
		default:
			if info.Size() > maxUploadBytes {
				skipped = append(skipped, path)
			} else {
				documents = append(documents, path)
			}
		}
	}

	for _, group := range PlanGroups(media, albumSize) {
		if err := u.sendGroup(ctx, chatID, group, caption); err != nil {
			return sent, skipped, err
		}
		sent += len(group)
	}

	for _, path := range documents {
		fields := map[string]string{"chat_id": chatID}
		if caption != "" {
			fields["caption"] = caption
		}
		if _, err := u.API.CallMultipart(ctx, "sendDocument", fields, map[string]string{"document": path}); err != nil {
			return sent, skipped, err
		}
		sent++
	}

	return sent, skipped, nil
}

// sendGroup 发送一个相册分组。
//
// 单项由 sendSingle 直接发送。多项走 sendMediaGroup；相册对图片格式的
// 处理比单张更严格，整组失败且原因是图片处理时，逐项重发，避免一张坏图
// 中断整批。逐项发送时仍保留首项 caption。
func (u *Uploader) sendGroup(ctx context.Context, chatID string, group []FileInfo, caption string) error {
	if len(group) == 1 {
		return u.sendSingle(ctx, chatID, group[0], caption)
	}

	items := make([]tgapi.Media, 0, len(group))
	for i, file := range group {
		item := tgapi.Media{Kind: kindName(file.Kind), Path: file.Path}
		if i == 0 {
			item.Caption = caption
		}
		items = append(items, item)
	}
	_, err := u.API.SendMediaGroup(ctx, chatID, items)
	if err == nil {
		return nil
	}
	if !isImageProcessingError(err) {
		return err
	}
	for i, file := range group {
		itemCaption := ""
		if i == 0 {
			itemCaption = caption
		}
		if err := u.sendSingle(ctx, chatID, file, itemCaption); err != nil {
			return err
		}
	}
	return nil
}

// sendSingle 按单张照片或视频发送。
//
// sendPhoto 遇到图片处理失败时降级为 document，保证单个文件仍能送达；
// 其他错误（鉴权、网络等）原样返回。
func (u *Uploader) sendSingle(ctx context.Context, chatID string, file FileInfo, caption string) error {
	method, field := "sendPhoto", "photo"
	if file.Kind == Video {
		method, field = "sendVideo", "video"
	}
	fields := map[string]string{"chat_id": chatID}
	if caption != "" {
		fields["caption"] = caption
	}
	_, err := u.API.CallMultipart(ctx, method, fields, map[string]string{field: file.Path})
	if err == nil {
		return nil
	}
	if method != "sendPhoto" || !isImageProcessingError(err) {
		return err
	}

	docFields := map[string]string{"chat_id": chatID}
	if caption != "" {
		docFields["caption"] = caption
	}
	_, docErr := u.API.CallMultipart(ctx, "sendDocument", docFields, map[string]string{"document": file.Path})
	return docErr
}

// isImageProcessingError 判断 Telegram 是否因图片解码/尺寸问题拒绝请求。
//
// 这类错误与文件内容有关，换 document 方式发送通常可以成功；其他 400
// （如 chat 不存在、caption 超长）不能靠降级解决。
func isImageProcessingError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "image_process_failed") ||
		strings.Contains(text, "photo_invalid_dimensions")
}
