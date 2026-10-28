package douyinsign

import "testing"

func TestSM3ABC(t *testing.T) {
	const want = "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0"
	if got := SM3HexDigest([]byte("abc")); got != want {
		t.Fatalf("%s", got)
	}
	arr := SM3ToArray([]byte("abc"))
	if len(arr) != 32 || arr[0] != 0x66 || arr[31] != 0xE0 {
		t.Fatalf("%v", arr)
	}
}
