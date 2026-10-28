// Package xhs 实现小红书笔记后端解析与下载。
//
// 口径沿用 Python 版 core/xhs_downloader.py：
//   - 笔记详情来自页面内嵌的 window.__INITIAL_STATE__，先归一化 JSON 值
//     语境的 undefined 再解码，取 note.noteDetailMap 的第一条；
//   - 图片优先原图 fileId 直链，回退 urlDefault/urlPre/infoList；
//   - 实况照片的图片与视频按 imageList 下标配对（001.heic + 001.mp4）；
//   - 笔记级视频从 h264 流里选画质最高的一档，落盘 video.mp4；
//   - 失效短链会被重定向到首页且 noteDetailMap 为空，需与“页面结构变更”
//     区分开。
//
// 本文件只负责纯解析：URL/状态提取、JSON 归一化与结构映射，不发起网络
// 请求。下载入口见 xhs.go。
package xhs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/backtraxe/downloader-go/internal/media"
)

// ErrNoNoteData 表示 __INITIAL_STATE__ 中缺少 noteDetailMap 或其为空。
var ErrNoNoteData = errors.New("xhs: no note data")

// ErrNoInitialState 表示页面中没有 window.__INITIAL_STATE__。
var ErrNoInitialState = errors.New("xhs: no __INITIAL_STATE__")

// Image 是笔记中的一张图片。Ext 为带前导点的落盘扩展名；无法从 URL
// 判断时为空串，由 httpclient.DownloadFile 按 Content-Type 修正。
type Image struct {
	URL string
	Ext string
}

// Pair 是一张实况照片（图片 + 配套视频直链）。
type Pair struct {
	ImageURL string
	VideoURL string
}

// Note 是从 __INITIAL_STATE__ 提取出的笔记信息。
type Note struct {
	ID       string
	Title    string
	Desc     string
	Author   string
	Images   []Image
	VideoURL string
	// LivePairs 与 Images 按 imageList 下标对应，实况视频沿用图片序号。
	LivePairs []Pair

	// tags 用于 title/desc 都为空时的目录基名兜底（Python tagList 口径）。
	tags []string
	// liveByIndex 与 Images 等长，记录每张图对应的实况视频（可为空）。
	liveByIndex []string
}

// initialStatePattern 与 Python 正则等价：贪婪匹配到最后一个 } 后接
// </script>，避免被状态内部的 }</script> 截断。
var initialStatePattern = regexp.MustCompile(`(?s)window\.__INITIAL_STATE__\s*=\s*(\{.*\})\s*</script>`)

// undefinedPattern 只替换 JSON 值语境的 undefined：前一个字符是 :、[、,
// 或空白，且后接词边界。紧跟在引号/其他字符后的字符串内容不受影响。
var undefinedPattern = regexp.MustCompile(`([\[:,\s])\s*undefined\b`)

// notePathPattern 匹配带作品 ID 的笔记页路径。
var notePathPattern = regexp.MustCompile(`(?i)/(?:explore|discovery/item|notes)/[^/]+`)

// tagMarkerPattern 把 "#tag[话题]#" 归一化为 "#tag"。
var tagMarkerPattern = regexp.MustCompile(`#([^\s#\[\]]+)\[话题\]#`)

// HasXsecToken 判断链接 query 是否带非空 xsec_token。
func HasXsecToken(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return strings.TrimSpace(u.Query().Get("xsec_token")) != ""
}

// NormalizeUndefined 把值语境的 undefined 替换为 null。
//
// 与 core/utils.py:normalize_xhs_state_json 的匹配口径一致，但不额外插入
// 空格，保证归一化结果是最小改动（仍是合法 JSON）。
func NormalizeUndefined(raw string) string {
	return undefinedPattern.ReplaceAllString(raw, "${1}null")
}

// ParseInitialState 从页面 HTML 中提取第一条笔记。
//
// 找不到 window.__INITIAL_STATE__ 返回 ErrNoInitialState；noteDetailMap
// 缺失或为空返回 ErrNoNoteData；JSON 损坏时返回带原因的错误。
func ParseInitialState(html string) (*Note, error) {
	match := initialStatePattern.FindStringSubmatch(html)
	if match == nil {
		return nil, ErrNoInitialState
	}
	stateJSON := NormalizeUndefined(match[1])

	var top struct {
		Note struct {
			NoteDetailMap json.RawMessage `json:"noteDetailMap"`
		} `json:"note"`
	}
	if err := json.Unmarshal([]byte(stateJSON), &top); err != nil {
		return nil, fmt.Errorf("xhs: decode initial state: %w", err)
	}

	key, detail, err := firstDetail(top.Note.NoteDetailMap)
	if err != nil {
		return nil, err
	}

	var entry struct {
		Note struct {
			NoteID string `json:"noteId"`
			Title  string `json:"title"`
			Desc   string `json:"desc"`
			User   struct {
				Nickname string `json:"nickname"`
				NickName string `json:"nickName"`
			} `json:"user"`
			ImageList []rawImage `json:"imageList"`
			Video     *rawVideo  `json:"video"`
			TagList   []struct {
				Name string `json:"name"`
			} `json:"tagList"`
		} `json:"note"`
	}
	if err := json.Unmarshal(detail, &entry); err != nil {
		return nil, fmt.Errorf("xhs: decode note detail: %w", err)
	}

	note := &Note{
		ID:     key,
		Title:  entry.Note.Title,
		Desc:   entry.Note.Desc,
		Author: entry.Note.User.Nickname,
	}
	if note.ID == "" {
		note.ID = entry.Note.NoteID
	}
	if note.Author == "" {
		note.Author = entry.Note.User.NickName
	}
	for _, tag := range entry.Note.TagList {
		if tag.Name != "" {
			note.tags = append(note.tags, tag.Name)
		}
	}

	note.Images = make([]Image, 0, len(entry.Note.ImageList))
	note.liveByIndex = make([]string, len(entry.Note.ImageList))
	for index, raw := range entry.Note.ImageList {
		imgURL := ensureHTTPS(imageURL(raw))
		note.Images = append(note.Images, Image{
			URL: imgURL,
			Ext: media.GuessExtension(imgURL, "", ""),
		})
		liveURL := ensureHTTPS(livePhotoURL(raw))
		note.liveByIndex[index] = liveURL
		if liveURL != "" {
			note.LivePairs = append(note.LivePairs, Pair{ImageURL: imgURL, VideoURL: liveURL})
		}
	}
	if entry.Note.Video != nil {
		note.VideoURL = ensureHTTPS(videoURL(*entry.Note.Video))
	}
	return note, nil
}

// firstDetail 按 JSON 对象顺序取 noteDetailMap 的第一条键值。
//
// 不用 map[string]json.RawMessage 解码：Go 的 map 遍历顺序随机，而
// Python 取的是插入顺序的第一条。这里用 Token 流保序。
func firstDetail(raw json.RawMessage) (string, json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil, ErrNoNoteData
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return "", nil, fmt.Errorf("xhs: decode noteDetailMap: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return "", nil, ErrNoNoteData
	}
	if !dec.More() {
		return "", nil, ErrNoNoteData
	}
	keyToken, err := dec.Token()
	if err != nil {
		return "", nil, fmt.Errorf("xhs: decode noteDetailMap key: %w", err)
	}
	key, ok := keyToken.(string)
	if !ok {
		return "", nil, ErrNoNoteData
	}
	var detail json.RawMessage
	if err := dec.Decode(&detail); err != nil {
		return "", nil, fmt.Errorf("xhs: decode note detail: %w", err)
	}
	return key, detail, nil
}

// IsHomepageLanding 判断最终落地页是否为首页且没有任何笔记数据。
//
// 失效短链会被 307 到 www.xiaohongshu.com（或 /explore），页面 200 正常、
// 无 404/风控文案，但 noteDetailMap 为空。路径带作品 ID 的笔记页即使
// noteDetailMap 为空也不算首页落地（那通常是 404/风控，另行提示）。
func IsHomepageLanding(finalURL, body string) bool {
	if !noteDetailMapEmpty(body) {
		return false
	}
	path := ""
	if u, err := url.Parse(strings.TrimSpace(finalURL)); err == nil {
		path = strings.TrimRight(u.Path, "/")
	}
	if path == "" || path == "/explore" {
		return true
	}
	return !notePathPattern.MatchString(path)
}

// noteDetailMapEmpty 判断页面/JSON 中的 noteDetailMap 是否缺失或为空。
// body 既可以是完整 HTML，也可以是测试里直接给的 JSON。
func noteDetailMapEmpty(body string) bool {
	raw := ""
	if match := initialStatePattern.FindStringSubmatch(body); match != nil {
		raw = match[1]
	} else {
		trimmed := strings.TrimSpace(body)
		if strings.HasPrefix(trimmed, "{") {
			raw = trimmed
		}
	}
	if raw == "" {
		return true
	}
	var top struct {
		Note struct {
			NoteDetailMap map[string]json.RawMessage `json:"noteDetailMap"`
		} `json:"note"`
	}
	if err := json.Unmarshal([]byte(NormalizeUndefined(raw)), &top); err != nil {
		return true
	}
	return len(top.Note.NoteDetailMap) == 0
}

// rawImage 对应 imageList 中的一项；只声明解析需要的字段。
type rawImage struct {
	FileID     string `json:"fileId"`
	URLDefault string `json:"urlDefault"`
	URLPre     string `json:"urlPre"`
	URL        string `json:"url"`
	InfoList   []struct {
		URL string `json:"url"`
	} `json:"infoList"`
	LivePhoto bool                   `json:"livePhoto"`
	Stream    map[string][]rawStream `json:"stream"`
}

// rawVideo 对应 note.video。
type rawVideo struct {
	URL   string `json:"url"`
	Media struct {
		Stream struct {
			H264 []rawStream `json:"h264"`
		} `json:"stream"`
	} `json:"media"`
}

// rawStream 是 h264/实况视频流的一档。
type rawStream struct {
	MasterURL    string  `json:"masterUrl"`
	QualityType  string  `json:"qualityType"`
	VideoBitrate float64 `json:"videoBitrate"`
}

// qualityRank 把 qualityType 映射为可比较的画质档位（LD < SD < HD）。
func qualityRank(s rawStream) (int, float64) {
	switch strings.ToUpper(s.QualityType) {
	case "LD":
		return 1, s.VideoBitrate
	case "SD":
		return 2, s.VideoBitrate
	case "HD":
		return 3, s.VideoBitrate
	default:
		return 0, s.VideoBitrate
	}
}

// bestStream 选画质最高的一档；无 masterUrl 的候选跳过。
func bestStream(streams []rawStream) string {
	best := ""
	var bestRank int
	var bestBitrate float64
	for _, stream := range streams {
		if stream.MasterURL == "" {
			continue
		}
		rank, bitrate := qualityRank(stream)
		if best == "" || rank > bestRank || (rank == bestRank && bitrate > bestBitrate) {
			best = stream.MasterURL
			bestRank = rank
			bestBitrate = bitrate
		}
	}
	return best
}

// imageURL 复刻 Python extract_image_url 的优先级：
// fileId 原图直链 → urlDefault → urlPre → url → infoList[0].url。
func imageURL(img rawImage) string {
	if img.FileID != "" {
		return "https://ci.xiaohongshu.com/" + img.FileID
	}
	if img.URLDefault != "" {
		return img.URLDefault
	}
	if img.URLPre != "" {
		return img.URLPre
	}
	if img.URL != "" {
		return img.URL
	}
	for _, info := range img.InfoList {
		if info.URL != "" {
			return info.URL
		}
	}
	return ""
}

// livePhotoURL 从实况照片的 stream 中取首个非空档位（EF4→EF7）的最高画质。
func livePhotoURL(img rawImage) string {
	if !img.LivePhoto {
		return ""
	}
	for _, key := range []string{"EF4", "EF5", "EF6", "EF7"} {
		if candidate := bestStream(img.Stream[key]); candidate != "" {
			return candidate
		}
	}
	return ""
}

// videoURL 从 h264 流里选最高画质；无 h264 时回退顶层 url。
func videoURL(video rawVideo) string {
	if len(video.Media.Stream.H264) > 0 {
		return bestStream(video.Media.Stream.H264)
	}
	return video.URL
}

// ensureHTTPS 把协议相对（//）与 http:// 的媒体链接统一升级为 https://。
// 小红书 CDN 图片直链常以 http:// 返回，部分节点仅稳定支持 HTTPS。
func ensureHTTPS(rawURL string) string {
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	if strings.HasPrefix(rawURL, "http://") {
		return "https://" + strings.TrimPrefix(rawURL, "http://")
	}
	return rawURL
}
