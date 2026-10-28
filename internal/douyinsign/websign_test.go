package douyinsign

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
	"testing"
)

func TestEncodePairs(t *testing.T) {
	if got := EncodePairs([][2]string{{"a", "1 2"}}); got != "a=1%202" {
		t.Fatal(got)
	}
	if got := EncodePairs([][2]string{{"a", "*-._x"}}); got != "a=*-._x" {
		t.Fatal(got)
	}
}

func TestSignFormula(t *testing.T) {
	pairs := [][2]string{{"aweme_id", "1"}, {"a_bogus", "xyz+/="}}
	query, sig, headers := Sign(pairs, "uid_123", 1791914678)
	body := query[:strings.Index(query, "&"+SignatureParam)]
	sum := md5.Sum([]byte("uid_123_1791914678_" + Salt + "_" + body))
	if want := hex.EncodeToString(sum[:]); sig != want {
		t.Fatalf("%s want %s", sig, want)
	}
	if headers[UIFIDParam] != "uid_123" {
		t.Fatalf("%v", headers)
	}
}

func TestPickUIFID(t *testing.T) {
	if got := PickUIFID(map[string]string{"UIFID_TEMP": "t"}); got != "t" {
		t.Fatal(got)
	}
	if got := PickUIFID(map[string]string{"foo": "bar"}); got != "" {
		t.Fatal(got)
	}
}
