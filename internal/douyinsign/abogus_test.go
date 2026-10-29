// Tests for the a_bogus port. The golden value is the output of the Python
// implementation under random.Random(42) at a fixed clock; the RNG fixture
// makes the noise-dependent bytes reproducible byte for byte.
package douyinsign

import (
	"encoding/json"
	"os"
	"testing"
)

const goldenABogus = "QJMcvtyjEoRcPVFG8Kn3et3lhy9ANPSymtT/WuFPHxYfL10GSRPGTPGvGxol4qI0HWBsie3HZdMAbdncpUU0pqHkomkvSPhWvU5CV80LhqwRGMJQLrbTezXFuwMn054qe55UiARI8Urq6VxAkrQD/B59SKLFQbSBMpxbk2YcT9sg1MyAE1cHPBtdNhGbUn54"

const goldenNowMS int64 = 1791914678000

type fixedRNG struct {
	vals []float64
	i    int
}

func (f *fixedRNG) Float64() float64 {
	v := f.vals[f.i]
	f.i++
	return v
}

func loadPythonRandom(t *testing.T) []float64 {
	t.Helper()
	raw, err := os.ReadFile("testdata/py_random_42.json")
	if err != nil {
		t.Fatal(err)
	}
	var vals []float64
	if err := json.Unmarshal(raw, &vals); err != nil {
		t.Fatal(err)
	}
	return vals
}

func TestABogusGolden(t *testing.T) {
	a := &ABogus{
		UserAgent:   "test-ua",
		BrowserInfo: DefaultBrowserInfo,
		PageID:      PageID,
		AID:         AID,
		RNG:         &fixedRNG{vals: loadPythonRandom(t)},
	}
	got, err := a.GetValue("aweme_id=1&aid=6383", SignOptions{NowMS: goldenNowMS})
	if err != nil || got != goldenABogus {
		t.Fatalf("err=%v got=%s", err, got)
	}
}

func TestABogusRejectsPreEpochClock(t *testing.T) {
	a := NewABogus("ua")
	if _, err := a.GetValue("k=1", SignOptions{NowMS: 0}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := a.GetValue("k=1", SignOptions{NowMS: 1}); err == nil {
		t.Fatal("expected error for clock before the fortnight epoch")
	}
}

func TestNewABogusDefaults(t *testing.T) {
	a := NewABogus("ua")
	if a.PageID != PageID || a.AID != AID || a.BrowserInfo != DefaultBrowserInfo {
		t.Fatalf("defaults not applied: %+v", a)
	}
	if a.RNG == nil {
		t.Fatal("RNG not set")
	}
	if AID != 6383 || PageID != 6241 {
		t.Fatalf("aid=%d page_id=%d", AID, PageID)
	}
}

func TestABogusMultipartBodyBlanked(t *testing.T) {
	withBody := &ABogus{
		UserAgent:   "test-ua",
		BrowserInfo: DefaultBrowserInfo,
		PageID:      PageID,
		AID:         AID,
		RNG:         &fixedRNG{vals: loadPythonRandom(t)},
	}
	withoutBody := &ABogus{
		UserAgent:   "test-ua",
		BrowserInfo: DefaultBrowserInfo,
		PageID:      PageID,
		AID:         AID,
		RNG:         &fixedRNG{vals: loadPythonRandom(t)},
	}
	got, err := withBody.GetValue("k=1", SignOptions{
		Body:        "not-hashed",
		ContentType: "multipart/form-data; boundary=xyz",
		NowMS:       goldenNowMS,
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := withoutBody.GetValue("k=1", SignOptions{NowMS: goldenNowMS})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("multipart body was hashed: got=%s want=%s", got, want)
	}
}
