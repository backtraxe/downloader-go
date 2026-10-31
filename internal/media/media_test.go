package media

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestSniffImageFormat(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want ImageFormat
	}{
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0}, ImageFormatJPEG},
		{"png", []byte("\x89PNG\r\n\x1a\n\x00\x00"), ImageFormatPNG},
		{"gif", []byte("GIF89a"), ImageFormatGIF},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), ImageFormatWebP},
		{"heic", append([]byte{0, 0, 0, 0x18}, []byte("ftypheic")...), ImageFormatHEIC},
		{"avif", append([]byte{0, 0, 0, 0x18}, []byte("ftypavif")...), ImageFormatAVIF},
		{"unknown", []byte("not-an-image"), ImageFormatUnknown},
		{"short", []byte{0xff}, ImageFormatUnknown},
	}
	for _, c := range cases {
		if got := SniffImageFormat(c.head); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestDetectImageFormat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(p, []byte{0xff, 0xd8, 0xff, 0xe0}, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DetectImageFormat(p); got != ImageFormatJPEG {
		t.Fatalf("got %v want JPEG", got)
	}
	if got := DetectImageFormat(filepath.Join(dir, "missing")); got != ImageFormatUnknown {
		t.Fatalf("missing file: got %v want Unknown", got)
	}
}

func TestGuessExtension(t *testing.T) {
	if got := GuessExtension("https://x/a.webp?x=1", "text/html", ".jpg"); got != ".webp" {
		t.Fatal(got)
	}
	if got := GuessExtension("https://x/a", "image/heic", ".jpg"); got != ".heic" {
		t.Fatal(got)
	}
	if got := GuessExtension("https://x/a", "text/html", ".jpg"); got != ".jpg" {
		t.Fatal(got)
	}
}

func TestGuessExtensionMore(t *testing.T) {
	cases := []struct {
		url, ct, def, want string
	}{
		{"https://x/a.JPG#f", "text/html", ".jpg", ".jpg"},
		{"https://x/a.jpeg", "", ".png", ".jpeg"},
		{"https://x/a.gifx", "", ".jpg", ".jpg"},
		{"https://x/a.mov", "", ".jpg", ".mov"},
		{"https://x/a", "image/jpeg; charset=binary", ".png", ".jpg"},
		{"https://x/a", "text/html; charset=utf-8", ".png", ".png"},
		{"https://x/a", "image/heif", ".jpg", ".heic"},
		{"https://x/a", "video/quicktime", ".mp4", ".mov"},
		{"https://x/a", "", ".jpg", ".jpg"},
	}
	for _, c := range cases {
		if got := GuessExtension(c.url, c.ct, c.def); got != c.want {
			t.Fatalf("%s %s: %q want %q", c.url, c.ct, got, c.want)
		}
	}
}

func TestIsHTMLContent(t *testing.T) {
	html := []string{"text/html", "text/html; charset=utf-8", " application/xhtml+xml "}
	for _, ct := range html {
		if !IsHTMLContent(ct) {
			t.Fatalf("%q should be html", ct)
		}
	}
	notHTML := []string{"image/jpeg", "application/json", ""}
	for _, ct := range notHTML {
		if IsHTMLContent(ct) {
			t.Fatalf("%q should not be html", ct)
		}
	}
}

func TestDecodeDimensions(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 10, 20))); err != nil {
		t.Fatal(err)
	}
	w, h, ok := DecodeDimensions(bytes.NewReader(buf.Bytes()))
	if !ok || w != 10 || h != 20 {
		t.Fatalf("%d %d %v", w, h, ok)
	}

	if _, _, ok := DecodeDimensions(bytes.NewReader([]byte("not an image"))); ok {
		t.Fatal("garbage decoded")
	}
}

func TestDecodeDimensionsJPEGAndGIF(t *testing.T) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 33, 44)), nil); err != nil {
		t.Fatal(err)
	}
	if w, h, ok := DecodeDimensions(&jpg); !ok || w != 33 || h != 44 {
		t.Fatalf("jpeg %d %d %v", w, h, ok)
	}

	var g bytes.Buffer
	if err := gif.Encode(&g, image.NewRGBA(image.Rect(0, 0, 7, 9)), nil); err != nil {
		t.Fatal(err)
	}
	if w, h, ok := DecodeDimensions(&g); !ok || w != 7 || h != 9 {
		t.Fatalf("gif %d %d %v", w, h, ok)
	}
}

func TestDecorativeNameWordBoundary(t *testing.T) {
	if !IsDecorativeName("site-logo.png") {
		t.Fatal("logo")
	}
	if IsDecorativeName("iconic-photo.jpg") {
		t.Fatal("iconic false positive")
	}
}

func TestDecorativeNameCases(t *testing.T) {
	decorative := []string{
		"logo.png", "LOGO.PNG", "a/avatar_1.jpg", "avatar-2.png",
		"qrcode.jpg", "qr_code.png", "emotion.gif", "emoji.webp",
		"beian.png", "icon.svg", "x.icon.png", "图标_icon.png",
	}
	for _, name := range decorative {
		if !IsDecorativeName(name) {
			t.Fatalf("%q should be decorative", name)
		}
	}
	normal := []string{"photo.jpg", "myicon.jpg", "icons.jpg", "avatars.jpg", "标志logo副本.png", "中文图片.png"}
	for _, name := range normal {
		if IsDecorativeName(name) {
			t.Fatalf("%q should not be decorative", name)
		}
	}
}

func TestIsDecorativeFile(t *testing.T) {
	dir := t.TempDir()

	write := func(name string, img image.Image) string {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}

	small := write("small.png", image.NewRGBA(image.Rect(0, 0, 10, 20)))
	if !IsDecorativeFile(small, 300, 20*1024) {
		t.Fatal("small dimensions should be decorative")
	}
	large := write("large.png", image.NewRGBA(image.Rect(0, 0, 400, 500)))
	if IsDecorativeFile(large, 300, 1<<30) {
		t.Fatal("large dimensions should not be filtered by bytes")
	}

	// 尺寸无法解析时按体积过滤。
	tinyRaw := filepath.Join(dir, "tiny.bin")
	if err := os.WriteFile(tinyRaw, []byte("xx"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsDecorativeFile(tinyRaw, 300, 20*1024) {
		t.Fatal("tiny unknown file should be decorative")
	}
	bigRaw := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(bigRaw, bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsDecorativeFile(bigRaw, 300, 1024) {
		t.Fatal("big unknown file should not be decorative")
	}

	if IsDecorativeFile(filepath.Join(dir, "missing.png"), 300, 20*1024) {
		t.Fatal("missing file should not be decorative")
	}
}
