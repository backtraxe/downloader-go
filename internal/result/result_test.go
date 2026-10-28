package result

import (
	"strings"
	"testing"
)

func TestFromCounts(t *testing.T) {
	cases := []struct {
		name string
		in   Counts
		want Status
	}{
		{"downloaded", Counts{Downloaded: 3}, Downloaded},
		{"partial", Counts{Downloaded: 2, Failed: 1}, Partial},
		{"failed", Counts{Failed: 2}, Failed},
		{"skipped", Counts{Skipped: 1}, Skipped},
		{"filtered", Counts{Filtered: 2}, NoMedia},
		{"empty", Counts{}, NoMedia},
		{"downloaded beats skipped", Counts{Downloaded: 1, Skipped: 2}, Downloaded},
		{"failed beats skipped", Counts{Failed: 1, Skipped: 2}, Failed},
		{"skipped beats filtered", Counts{Skipped: 1, Filtered: 2}, Skipped},
		{"downloaded beats filtered", Counts{Downloaded: 1, Filtered: 2}, Downloaded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FromCounts(tc.in); got.Status != tc.want {
				t.Fatalf("status=%s want %s", got.Status, tc.want)
			}
		})
	}
}

func TestOK(t *testing.T) {
	for _, s := range []Status{Downloaded, Partial, Skipped} {
		if !(Result{Status: s}).OK() {
			t.Fatalf("%s should be ok", s)
		}
	}
	for _, s := range []Status{Failed, NoMedia} {
		if (Result{Status: s}).OK() {
			t.Fatalf("%s should not be ok", s)
		}
	}
}

func TestStatusValues(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{Downloaded, "downloaded"},
		{Partial, "partial"},
		{Skipped, "skipped"},
		{Failed, "failed"},
		{NoMedia, "no_media"},
	}
	for _, tc := range cases {
		if string(tc.status) != tc.want {
			t.Fatalf("status=%q want %q", tc.status, tc.want)
		}
	}
}

func TestFromCountsMessage(t *testing.T) {
	cases := []struct {
		name string
		in   Counts
		want []string
	}{
		{"partial", Counts{Downloaded: 2, Failed: 1}, []string{"新增 2 项", "失败 1 项"}},
		{"mixed", Counts{Downloaded: 3, Skipped: 2, Failed: 1, Filtered: 4}, []string{"新增 3 项", "失败 1 项", "已存在 2 项", "过滤 4 项"}},
		{"empty", Counts{}, []string{"未发现"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := FromCounts(tc.in).Message
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("message=%q missing %q", msg, want)
				}
			}
		})
	}
}
