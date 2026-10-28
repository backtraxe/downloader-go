package telegram

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/backtraxe/downloader-go/internal/result"
)

// UploadPath 把 CLI 给出的目录或单个文件上传到 chatID。
//
// 目录只收集第一层的普通文件并按文件名排序，作为一个作品上传（相册 +
// document）；单文件直接上传，caption 为空时用不含扩展名的文件名作为
// 标题。返回成功文件数与被跳过的本地路径。
func UploadPath(ctx context.Context, u *Uploader, chatID, path, caption string) (sent int, skipped []string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, nil, fmt.Errorf("telegram: stat %s: %w", path, err)
	}

	files := make([]string, 0, 1)
	if info.IsDir() {
		entries, err := os.ReadDir(path)
		if err != nil {
			return 0, nil, fmt.Errorf("telegram: read dir %s: %w", path, err)
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				files = append(files, filepath.Join(path, entry.Name()))
			}
		}
		sort.Strings(files)
	} else {
		files = append(files, path)
		if caption == "" {
			base := filepath.Base(path)
			caption = strings.TrimSuffix(base, filepath.Ext(base))
		}
	}

	post := result.Post{Site: "", Author: "", Title: caption, Files: files}
	return u.Send(ctx, chatID, post)
}
