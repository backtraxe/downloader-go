package telegram

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/backtraxe/downloader-go/internal/media"
)

// Bot API 与预览处理相关阈值，与 Python 版 telegram/upload.py 一致。
const (
	// maxUploadBytes 是官方 Bot API 的单文件上限。
	maxUploadBytes = 50 << 20
	// maxPhotoBytes 是 Telegram photo 的上限，超过改按 document 发送。
	maxPhotoBytes = 10 << 20
	// videoTargetBytes 是视频压缩目标（给封装开销留 1MB 余量）。
	videoTargetBytes = 49 << 20
	// audioBitrate 固定音轨码率，从视频码率预算中扣除。
	audioBitrate = 96_000
	// maxPhotoDimSum 是照片宽高像素和的预览上限，超出先等比缩小。
	maxPhotoDimSum = 10000
	// maxPhotoAspectRatio 是 Telegram 照片的宽高比上限（长边/短边）。
	maxPhotoAspectRatio = 20
	// maxPhotoCompressRounds 是大图逐轮缩放的上限。
	maxPhotoCompressRounds = 8
	// photoScaleFactor 是每轮缩放比例。
	photoScaleFactor = 0.85
)

// jpegQualities 是 ffmpeg 的 JPEG 质量阶梯（值越小质量越高）。
var jpegQualities = []string{"2", "4", "6"}

// Runner 执行外部命令并返回 stdout/stderr。测试注入假实现即可离线运行。
type Runner func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)

// PreparePhoto 在需要时把照片转成 Telegram 可接受的 JPEG 预览。
//
// 判断依据是文件头而不是扩展名：小红书等站点常把 WebP/HEIC 原图保存成
// .jpg，按扩展名放行会在 sendMediaGroup 里触发 IMAGE_PROCESS_FAILED。
// JPEG/PNG 原样发送；WebP、HEIC/HEIF、AVIF、无法识别或无法读取尺寸的
// 图片，以及体积 >10MB、宽高和 >10000 的图片，交给 ffmpeg 转成 JPEG。
// 宽高比超过 Telegram 上限 20 时等比缩放无法修复，直接按文件发送。
// ffmpeg 缺失、执行失败或压缩后仍超标时返回原始路径且 asDocument=true，
// 由调用方降级为文件发送；这种情况不返回 error。
//
// 返回的 prepared 与 path 不同时是临时产物，由调用方负责删除。
func (u *Uploader) PreparePhoto(ctx context.Context, path string) (prepared string, asDocument bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("telegram: stat %s: %w", path, err)
	}

	format := media.DetectImageFormat(path)
	unsupported := format != media.ImageFormatJPEG && format != media.ImageFormatPNG
	width, height, dimsKnown := photoDimensions(path)
	// JPEG/PNG 也可能损坏或使用 Go 无法解码的变体；读不出尺寸时交给
	// ffmpeg 验证，避免把坏图直接塞进相册。
	if !unsupported && !dimsKnown {
		unsupported = true
	}

	oversized := info.Size() > maxPhotoBytes
	if dimsKnown {
		if width+height > maxPhotoDimSum || exceedsAspectRatio(width, height) {
			oversized = true
		}
	}
	if !unsupported && !oversized {
		return path, false, nil
	}
	// 长图等比缩放后宽高比不变，无法满足 Telegram 限制，按文件发送。
	if dimsKnown && exceedsAspectRatio(width, height) {
		return path, true, nil
	}

	out, ok := u.compressPhoto(ctx, path, unsupported, width, height, dimsKnown)
	if !ok {
		return path, true, nil
	}
	return out, false, nil
}

// exceedsAspectRatio 判断长边/短边是否超过 Telegram 照片上限。
func exceedsAspectRatio(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	if width < height {
		width, height = height, width
	}
	return width > maxPhotoAspectRatio*height
}

// compressPhoto 执行格式转换或大图缩放/质量阶梯。
//
// convert=true 时先按最高质量导出单帧 JPEG（WebP/HEIC/AVIF/未知格式），
// 再重新量尺寸；其余情况直接按体积与宽高和做缩放/质量阶梯。
// 返回 ok=false 表示 ffmpeg 不可用、失败或最终产物仍超限。
func (u *Uploader) compressPhoto(ctx context.Context, path string, convert bool, width, height int, dimsKnown bool) (string, bool) {
	out, err := u.workFile(path, ".jpg")
	if err != nil {
		return "", false
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(out)
		}
	}()
	base := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", path, "-frames:v", "1"}

	if convert {
		// Termux 的 libheif 不带 CLI，ffmpeg 原生解 HEIF/WebP/AVIF 单帧即可。
		args := append(append([]string{}, base...), "-q:v", "2", out)
		if err := u.runFFmpeg(ctx, args); err != nil {
			return "", false
		}
		// JPEG 产物也要重新量尺寸：宽幅全景可能体积达标但宽高和超限，
		// 极端长图也可能仍超过 Telegram 的宽高比限制。
		if convertedW, convertedH, known := photoDimensions(out); known {
			if exceedsAspectRatio(convertedW, convertedH) {
				return "", false
			}
			width, height, dimsKnown = convertedW, convertedH, true
		}
		if sizeAtMost(out, maxPhotoBytes) && (!dimsKnown || width+height <= maxPhotoDimSum) {
			success = true
			return out, true
		}
	}

	canScale := dimsKnown && width+height > maxPhotoDimSum
	curW, curH := width, height
	if canScale {
		scale := float64(maxPhotoDimSum) / float64(width+height)
		curW = maxInt(1, int(float64(width)*scale))
		curH = maxInt(1, int(float64(height)*scale))
	}
	rounds := 1
	if canScale {
		rounds = maxPhotoCompressRounds
	}
	for round := 0; round < rounds; round++ {
		for _, quality := range jpegQualities {
			args := append([]string{}, base...)
			if canScale {
				args = append(args, "-vf", fmt.Sprintf("scale=%d:%d", curW, curH))
			}
			args = append(args, "-q:v", quality, out)
			if err := u.runFFmpeg(ctx, args); err != nil {
				return "", false
			}
			if sizeAtMost(out, maxPhotoBytes) {
				success = true
				return out, true
			}
		}
		if !canScale {
			break
		}
		curW = maxInt(1, int(float64(curW)*photoScaleFactor))
		curH = maxInt(1, int(float64(curH)*photoScaleFactor))
	}
	return "", false
}

// CompressVideo 用 ffmpeg 把视频压到约 targetBytes。
//
// 先用 ffprobe 读取时长（秒），按 (targetBytes*8/duration) - 96kbps 计算
// 视频码率，下限 200kbps；输出到 WorkDir 的 .mp4。ffprobe/ffmpeg 缺失、
// 时长无法解析或执行失败都返回 error，调用方据此跳过该文件。返回值是
// 临时产物，由调用方负责删除。
func (u *Uploader) CompressVideo(ctx context.Context, path string, targetBytes int64) (string, error) {
	probe := u.FFprobe
	if probe == "" {
		probe = "ffprobe"
	}
	stdout, stderr, err := u.run(ctx, probe,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1",
		path,
	)
	if err != nil {
		return "", fmt.Errorf("telegram: ffprobe %s: %w%s", path, err, stderrSuffix(stderr))
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(stdout)), 64)
	if err != nil || duration <= 0 {
		return "", fmt.Errorf("telegram: ffprobe %s: invalid duration %q", path, strings.TrimSpace(string(stdout)))
	}
	if targetBytes <= 0 {
		targetBytes = videoTargetBytes
	}
	bitrate := int64(float64(targetBytes)*8/duration) - audioBitrate
	if bitrate < 200_000 {
		bitrate = 200_000
	}

	out, err := u.workFile(path, ".mp4")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(out)
		}
	}()
	if err := u.runFFmpeg(ctx, []string{
		"-y",
		"-i", path,
		"-c:v", "libx264",
		"-b:v", strconv.FormatInt(bitrate, 10),
		"-c:a", "aac",
		"-b:a", "96k",
		out,
	}); err != nil {
		return "", err
	}
	if _, err := os.Stat(out); err != nil {
		return "", fmt.Errorf("telegram: ffmpeg produced no output: %w", err)
	}
	success = true
	return out, nil
}

// run 执行外部命令；Run 为空时用 exec.CommandContext。
func (u *Uploader) run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	if u.Run != nil {
		return u.Run(ctx, name, args...)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// runFFmpeg 以配置的 ffmpeg 路径（默认 "ffmpeg"）执行命令。
func (u *Uploader) runFFmpeg(ctx context.Context, args []string) error {
	ffmpeg := u.FFmpeg
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	_, stderr, err := u.run(ctx, ffmpeg, args...)
	if err != nil {
		return fmt.Errorf("telegram: ffmpeg: %w%s", err, stderrSuffix(stderr))
	}
	return nil
}

// workFile 在 WorkDir 生成一个该来源文件专属的输出名。
//
// WorkDir 为空时落在系统临时目录；用 CreateTemp 占位后删除，交给
// ffmpeg 创建，避免覆盖同名源文件。
func (u *Uploader) workFile(src, ext string) (string, error) {
	dir := u.WorkDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("telegram: create work dir %s: %w", dir, err)
	}
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	if stem == "" {
		stem = "media"
	}
	f, err := os.CreateTemp(dir, stem+"-*"+ext)
	if err != nil {
		return "", fmt.Errorf("telegram: allocate output in %s: %w", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("telegram: close placeholder %s: %w", name, err)
	}
	if err := os.Remove(name); err != nil {
		return "", fmt.Errorf("telegram: remove placeholder %s: %w", name, err)
	}
	return name, nil
}

// photoDimensions 读取图片宽高；格式不支持或读取失败时 known=false。
func photoDimensions(path string) (width, height int, known bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	return media.DecodeDimensions(f)
}

// sizeAtMost 判断文件存在且不超过 limit。
func sizeAtMost(path string, limit int64) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0 && info.Size() <= limit
}

// stderrSuffix 把 stderr 尾部拼进错误信息，便于诊断 ffmpeg 失败原因。
func stderrSuffix(stderr []byte) string {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		return ""
	}
	if len(msg) > 512 {
		msg = msg[len(msg)-512:]
	}
	return ": " + msg
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
