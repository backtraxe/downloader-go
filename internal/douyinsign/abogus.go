// Douyin's a_bogus, in pure Go.
//
// Ported from Evil0ctal/Douyin_TikTok_Download_API (Apache-2.0); the original
// copyright and license notice is retained. See LICENSE.apache in this
// directory.
// Upstream: src/dtk/signing/native/abogus.py @ main (pulled 2026-10-14)
// https://github.com/Evil0ctal/Douyin_TikTok_Download_API

package douyinsign

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

const (
	// AID is Douyin web's own app id; it travels in the signature body.
	AID = 6383
	// PageID is Douyin web's own page id; it travels in the signature body.
	PageID = 6241
	// DefaultBrowserInfo is the geometry a browser without a fingerprint
	// carries: Chrome on a 1080p display.
	DefaultBrowserInfo = "1920|947|1920|1032|1920|1032|1920|1080|Win32"

	// abogusSalt is appended to the query and body before hashing.
	abogusSalt = "dhzx"

	// payloadKey is the single byte the frame is sealed under.
	payloadKey = 0xD3

	// fortnightEpochMS is 2024-07-24T16:00:00Z; one field counts fortnights
	// from it.
	fortnightEpochMS int64 = 1_721_836_800_000

	// envFlags, detectFlags and nrFlags are what an untouched browser reports
	// about itself.
	envFlags    = 1
	detectFlags = 14
	nrFlags     = 0x21

	// tripwireLocked is the state of the SDK's own frozen tripwires.
	tripwireLocked = 3

	// callBucket is fn149's bucket for "this page has signed fewer than 140
	// times".
	callBucket = 6

	tripwireSet  = 0xB2
	tripwireFree = 0x4D

	// unset marks an unspecified maskPair override, matching Python's None.
	unset = -1
)

const (
	alphabetS3 = "ckdp1h4ZKsUB80/Mfvw36XIgR25+WQAlEi7NLboqYTOPuzmFjJnryx9HVGDaStCe"
	alphabetS4 = "Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe"
)

// headerMagic is the plaintext pair the header carries before noise dilution.
var headerMagic = [2]byte{3, 82}

// sdkVersion is "1.0.1.19-fix.01" as the SDK parses it.
var sdkVersion = [4]byte{1, 0, 1, 0}

// noiseMasks partition a byte together with dataMasks: 0x91|0x42|0x2C == 0xFF.
var (
	noiseMasks = [3]byte{0x91, 0x42, 0x2C}
	dataMasks  = [3]byte{0x6E, 0xBD, 0xD3}
)

// nrTag is the four-byte tag written to L40..L43.
var nrTag = [4]byte{0, 0, 0, 0}

// fieldOrder is how the fifty scalars are shuffled into the body.
var fieldOrder = []string{
	"L34", "L44", "L56", "L61", "L73", "L29", "L70", "L45", "L35", "L49",
	"L38", "L66", "L51", "L68", "L28", "L48", "L64", "L47", "L30", "L71",
	"L26", "L55", "L31", "L69", "L59", "L40", "L62", "L63", "L27", "L72",
	"L41", "L74", "L57", "L52", "L42", "L39", "L33", "L67", "L53", "L43",
	"L65", "L46", "L36", "L24", "L60", "L32", "L79", "L80", "L84", "L85",
}

// digestChain says where one hashed input leaves its three bytes among the
// fifty scalars.
type digestChain struct {
	slots   [3]string
	indices [2]int
	canary  [3]int
}

// digestChains binds the three hashed inputs to their slots. The canary triple
// is sentinel/offset/fallback for the third byte.
var digestChains = map[string]digestChain{
	"query":      {slots: [3]string{"L48", "L49", "L51"}, indices: [2]int{9, 18}, canary: [3]int{3, 11, 12}},
	"body":       {slots: [3]string{"L52", "L53", "L55"}, indices: [2]int{10, 19}, canary: [3]int{4, 8, 9}},
	"user_agent": {slots: [3]string{"L56", "L57", "L59"}, indices: [2]int{11, 21}, canary: [3]int{5, 12, 13}},
}

// headerNoiseBands is the 40-wide band fn143 draws from per browser family.
var headerNoiseBands = map[string]int{
	"chrome":  0,
	"firefox": 40,
	"safari":  81,
	"edge":    125,
	"huawei":  170,
	"other":   210,
}

// ABogus computes a_bogus for one browser identity.
type ABogus struct {
	UserAgent   string
	BrowserInfo string
	PageID      int
	AID         int
	RNG         RNG
}

// NewABogus returns a signer carrying Douyin web's identifiers and geometry,
// with noise from math/rand/v2.
func NewABogus(userAgent string) *ABogus {
	return &ABogus{
		UserAgent:   userAgent,
		BrowserInfo: DefaultBrowserInfo,
		PageID:      PageID,
		AID:         AID,
		RNG:         mathRNG{},
	}
}

// SignOptions is everything GetValue takes besides the query.
type SignOptions struct {
	Body        string
	ContentType string
	NowMS       int64
}

// GetValue returns the a_bogus value for one request.
//
// query is the query string as sent, without a_bogus itself. Body is hashed, so
// a POST signed with an empty body is a wrong signature; ContentType exists
// only for the multipart rule, where the SDK blanks the body because it is a
// FormData rather than text. A clock before the fortnight epoch is refused: one
// field counts fortnights from 2024-07-24 and has nowhere to put a negative.
func (a *ABogus) GetValue(query string, opts SignOptions) (string, error) {
	body := opts.Body
	if strings.Contains(strings.ToLower(opts.ContentType), "multipart/form-data") {
		body = ""
	}

	now := opts.NowMS
	if now < fortnightEpochMS {
		return "", fmt.Errorf("clock is before the a_bogus epoch: %d", now)
	}
	fields := a.fields(query, body, now)

	// Three of the four noise bytes below are environment reports rather than
	// noise. Argument evaluation order matters here and matches Python: the
	// probe and tripwire draws happen before maskPair makes its own.
	version := append(
		maskPair([2]byte{sdkVersion[0], sdkVersion[1]}, a.RNG, unset, unset),
		maskPair([2]byte{sdkVersion[2], sdkVersion[3]}, a.RNG, probeNoise(a.RNG), tripwireNoise(a.RNG))...,
	)

	// The checksum covers the version block and the fifty scalars, nothing
	// else.
	checksum := byte(0)
	for _, b := range version {
		checksum ^= b
	}
	for _, name := range fieldOrder {
		checksum ^= fields[name]
	}

	bodyBytes := make([]byte, 0, len(fieldOrder)+len(jsBytes(a.BrowserInfo))+len(jsBytes(tailFor(now)))+1)
	for _, name := range fieldOrder {
		bodyBytes = append(bodyBytes, fields[name])
	}
	bodyBytes = append(bodyBytes, jsBytes(a.BrowserInfo)...)
	bodyBytes = append(bodyBytes, jsBytes(tailFor(now))...)
	bodyBytes = append(bodyBytes, checksum)

	// The header's high noise byte is a browser-family report; the maskPair
	// draw follows it.
	header := maskPair(headerMagic, a.RNG, unset, headerNoise(a.UserAgent, a.RNG))
	frame := expandNoise(bodyBytes, a.RNG)
	sealed := rc4([]byte{payloadKey}, append(version, frame...))
	return encodeBase64(append(header, sealed...), alphabetS4), nil
}

// fields builds the fifty scalars by the names the offline decoder prints.
func (a *ABogus) fields(query, body string, nowMS int64) map[string]byte {
	digests := map[string][]int{
		"query":      digestOf(query),
		"body":       digestOf(body),
		"user_agent": userAgentDigest(a.UserAgent),
	}

	// ink is Date.now()-1: a liveness check the SDK plants on itself.
	ink := nowMS - 1
	fortnights := (nowMS - fortnightEpochMS) / (1000 * 60 * 60 * 24 * 14)
	infoBytes := jsBytes(a.BrowserInfo)
	tailBytes := jsBytes(tailFor(nowMS))

	fields := map[string]byte{
		"L24": 41,
		"L26": byte(fortnights),
		"L27": callBucket,
		"L28": 3,
		"L35": byte(envFlags & 0xFF),
		"L36": byte((envFlags / 256) & 0xFF),
		"L38": byte(nrFlags & 0xFF),
		"L39": byte((nrFlags >> 8) & 0xFF),
		"L66": tripwireLocked,
		"L79": byte(len(infoBytes) & 0xFF),
		"L80": byte((len(infoBytes) >> 8) & 0xFF),
		"L84": byte(len(tailBytes) & 0xFF),
		"L85": byte((len(tailBytes) >> 8) & 0xFF),
	}
	for index, value := range leBytes(nowMS, 6) {
		fields[fmt.Sprintf("L%d", 29+index)] = value
	}
	for index, value := range leBytes(detectFlags, 4) {
		fields[fmt.Sprintf("L%d", 44+index)] = value
	}
	for index, value := range nrTag {
		fields[fmt.Sprintf("L%d", 40+index)] = value
	}
	for index, value := range leBytes(ink, 6) {
		fields[fmt.Sprintf("L%d", 60+index)] = value
	}
	for index, value := range leBytes(int64(a.PageID), 4) {
		fields[fmt.Sprintf("L%d", 67+index)] = value
	}
	for index, value := range leBytes(int64(a.AID), 4) {
		fields[fmt.Sprintf("L%d", 71+index)] = value
	}
	for name, chain := range digestChains {
		written := chainBytes(chain, digests[name])
		for index, slot := range chain.slots {
			fields[slot] = written[index]
		}
	}
	return fields
}

// tailFor is the small decimal-plus-comma tail the body carries.
func tailFor(nowMS int64) string {
	return fmt.Sprintf("%d,", (nowMS+3)&0xFF)
}

// rc4 is the SDK's RC4, which is not textbook: the box is seeded descending and
// the key schedule multiplies instead of only adding.
func rc4(key, data []byte) []byte {
	var box [256]byte
	for i := 0; i < 256; i++ {
		box[255-i] = byte(i)
	}
	j := 0
	for i := 0; i < 256; i++ {
		j = (j*int(box[i]) + j + int(key[i%len(key)])) % 256
		box[i], box[j] = box[j], box[i]
	}

	out := make([]byte, len(data))
	i, j := 0, 0
	for index, b := range data {
		i = (i + 1) % 256
		j = (j + int(box[i])) % 256
		box[i], box[j] = box[j], box[i]
		out[index] = b ^ box[(int(box[i])+int(box[j]))%256]
	}
	return out
}

// encodeBase64 is standard base64 over one of the SDK's alphabets, with a
// literal "=" pad.
func encodeBase64(data []byte, alphabet string) string {
	var out strings.Builder
	for offset := 0; offset < len(data); offset += 3 {
		end := min(offset+3, len(data))
		chunk := data[offset:end]
		block := 0
		for _, b := range chunk {
			block = block<<8 | int(b)
		}
		block <<= 8 * (3 - len(chunk))
		digits := [4]int{(block >> 18) & 0x3F, (block >> 12) & 0x3F, (block >> 6) & 0x3F, block & 0x3F}
		for _, digit := range digits[:len(chunk)+1] {
			out.WriteByte(alphabet[digit])
		}
	}
	text := out.String()
	if pad := (4 - len(text)%4) % 4; pad > 0 {
		text += strings.Repeat("=", pad)
	}
	return text
}

// jsBytes turns a JavaScript string into bytes the way fn139 does: one byte per
// UTF-16 code unit below U+0100, two big-endian bytes above it.
func jsBytes(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, len(units)*2)
	for _, code := range units {
		if code&0xFF00 != 0 {
			out = append(out, byte(code>>8))
		}
		out = append(out, byte(code))
	}
	return out
}

// leBytes returns count little-endian bytes of value.
func leBytes(value int64, count int) []byte {
	out := make([]byte, count)
	for index := range out {
		out[index] = byte(value >> (8 * index))
	}
	return out
}

// canary returns the first digest byte at or after offset that is not sentinel,
// or fallback if every one is.
func canary(digest []int, offset, sentinel, fallback int) int {
	for _, b := range digest[offset:] {
		if b != sentinel {
			return b
		}
	}
	return fallback
}

// digestOf is SM3(SM3(text + SALT)).
func digestOf(text string) []int {
	first := SM3Hash([]byte(text + abogusSalt))
	return SM3ToArray(first[:])
}

// userAgentDigest is the third chain: RC4 the User-Agent, base64 it, hash it
// once.
func userAgentDigest(userAgent string) []int {
	key := []byte{byte(envFlags / 256), byte(envFlags % 256), byte(detectFlags % 256)}
	sealed := rc4(key, jsBytes(strings.TrimSpace(userAgent)))
	return SM3ToArray([]byte(encodeBase64(sealed, alphabetS3)))
}

// chainBytes returns the three bytes one chain contributes.
func chainBytes(chain digestChain, digest []int) [3]byte {
	return [3]byte{
		byte(digest[chain.indices[0]]),
		byte(digest[chain.indices[1]]),
		byte(canary(digest, chain.canary[0], chain.canary[1], chain.canary[2])),
	}
}

// familyOf picks the band fn143 would draw from for this User-Agent. Edge and
// Huawei also say "Chrome", so the specific names are tested first.
func familyOf(userAgent string) string {
	ua := strings.ToLower(userAgent)
	for _, name := range []string{"edg", "huawei", "firefox", "chrome", "safari"} {
		if strings.Contains(ua, name) {
			if name == "edg" {
				return "edge"
			}
			return name
		}
	}
	return "other"
}

// headerNoise is fn143: a browser-family report dressed as noise.
func headerNoise(userAgent string, rng RNG) int {
	base, ok := headerNoiseBands[familyOf(userAgent)]
	if !ok {
		base = headerNoiseBands["other"]
	}
	return (base + int(rng.Float64()*40)) & 0xFF
}

// probeNoise is fn144: below 110 the value passes through, above it the SDK
// forces it odd.
func probeNoise(rng RNG) int {
	value := int(rng.Float64() * 240)
	if value > 109 {
		return value + value%2 + 1
	}
	return value
}

// tripwireNoise is fn145: "my tripwires are present and locked", plus noise.
func tripwireNoise(rng RNG) int {
	return (int(rng.Float64()*255) & tripwireFree) | tripwireSet
}

// maskPair spreads two bytes over four, filling the other half of each with
// noise. low and high are the two noise bytes; unset means draw them together.
func maskPair(pair [2]byte, rng RNG, low, high int) []byte {
	noise := int(rng.Float64() * 65535)
	if low == unset {
		low = noise & 0xFF
	}
	if high == unset {
		high = (noise >> 8) & 0xFF
	}
	return []byte{
		byte((low & 0xAA) | (int(pair[0]) & 0x55)),
		byte((low & 0x55) | (int(pair[0]) & 0xAA)),
		byte((high & 0xAA) | (int(pair[1]) & 0x55)),
		byte((high & 0x55) | (int(pair[1]) & 0xAA)),
	}
}

// expandNoise turns three body bytes and one noise byte into four carrier
// bytes.
func expandNoise(body []byte, rng RNG) []byte {
	out := make([]byte, 0, (len(body)/3+1)*4)
	for offset := 0; offset < len(body); offset += 3 {
		end := min(offset+3, len(body))
		group := body[offset:end]
		if len(group) < 3 {
			// The SDK's tail branch, unreachable for bodies this builds.
			out = append(out, group[0])
			if len(group) > 1 && group[1] != 0 {
				out = append(out, group[1])
			}
			continue
		}
		noise := byte(int(rng.Float64()*1000) & 0xFF)
		for index := 0; index < 3; index++ {
			out = append(out, (noise&noiseMasks[index])|(group[index]&dataMasks[index]))
		}
		out = append(out,
			(group[0]&noiseMasks[0])|(group[1]&noiseMasks[1])|(group[2]&noiseMasks[2]))
	}
	return out
}
