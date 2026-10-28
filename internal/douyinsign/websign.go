// Douyin's x-secsdk-web-signature, in pure Go.
//
// Ported from Evil0ctal/Douyin_TikTok_Download_API (Apache-2.0); the original
// copyright and license notice is retained. See LICENSE.apache in this
// directory.
// Upstream: src/dtk/signing/native/websign.py @ main (pulled 2026-10-14)
// https://github.com/Evil0ctal/Douyin_TikTok_Download_API

package douyinsign

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	// UIFIDParam is the visitor id the signature is bound to.
	UIFIDParam = "uifid"
	// SignatureParam is the query parameter and header carrying the signature.
	SignatureParam = "x-secsdk-web-signature"
	// ExpireHeader is sent alongside the query parameters by Douyin's own pages.
	ExpireHeader = "x-secsdk-web-expire"
	// TimestampParam is appended to every signed query.
	TimestampParam = "timestamp"
	// Salt is string #39 of the SDK VM's table, per project (douyin_web).
	Salt = "A96D855A08C0A9707F8BEF0D9A527E4E"
)

// uifidCookieNames is where the SDK looks for the visitor id, in order, first
// non-empty wins. A Douyin mint yields UIFID_TEMP; the others are the same
// value under the spellings the SDK also accepts.
var uifidCookieNames = [...]string{
	"uifid",
	"uifid_temp",
	"uifidtemp",
	"UIFID",
	"UIFID_TEMP",
	"UIFIDTEMP",
}

const upperHex = "0123456789ABCDEF"

// PickUIFID returns the visitor id from the cookie jar, or "" if absent.
func PickUIFID(cookies map[string]string) string {
	for _, name := range uifidCookieNames {
		if value := cookies[name]; value != "" {
			return value
		}
	}
	return ""
}

// encodeComponent percent-encodes one side of a query pair like the JavaScript
// URLSearchParams the SDK uses. A-Z, a-z, 0-9 and "*-._" are left alone;
// everything else, including space (as %20, never "+"), is escaped.
func encodeComponent(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0x0F])
		}
	}
	return b.String()
}

// EncodePairs serializes pairs as k=v joined by "&", percent-encoding each
// side with the URLSearchParams safe set.
func EncodePairs(pairs [][2]string) string {
	parts := make([]string, len(pairs))
	for i, pair := range pairs {
		parts[i] = encodeComponent(pair[0]) + "=" + encodeComponent(pair[1])
	}
	return strings.Join(parts, "&")
}

// Sign returns the signed query, the signature and the headers for a request.
//
// pairs is everything that goes in the query before signing, in order. uifid
// and timestamp are appended here, because the signature covers them: a uifid
// already present keeps its position and is not duplicated, while timestamp is
// always appended.
func Sign(pairs [][2]string, uifid string, timestamp int64) (query, sig string, headers map[string]string) {
	stamp := strconv.FormatInt(timestamp, 10)
	covered := make([][2]string, len(pairs), len(pairs)+2)
	copy(covered, pairs)
	hasUIFID := false
	for _, pair := range covered {
		if pair[0] == UIFIDParam {
			hasUIFID = true
			break
		}
	}
	if !hasUIFID {
		covered = append(covered, [2]string{UIFIDParam, uifid})
	}
	covered = append(covered, [2]string{TimestampParam, stamp})
	query = EncodePairs(covered)
	sum := md5.Sum([]byte(fmt.Sprintf("%s_%s_%s_%s", uifid, stamp, Salt, query)))
	sig = hex.EncodeToString(sum[:])
	headers = map[string]string{
		UIFIDParam:     uifid,
		SignatureParam: sig,
		ExpireHeader:   stamp,
	}
	return query + "&" + SignatureParam + "=" + sig, sig, headers
}
