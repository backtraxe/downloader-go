// SM3 hash (GB/T 32905-2016), pure Go.
//
// Ported from Evil0ctal/Douyin_TikTok_Download_API (Apache-2.0); the original
// copyright and license notice is retained. See LICENSE.apache in this
// directory.
// Upstream: src/dtk/signing/native/sm3.py @ main (pulled 2026-10-14)
// https://github.com/Evil0ctal/Douyin_TikTok_Download_API
package douyinsign

import (
	"encoding/binary"
	"encoding/hex"
)

const (
	sm3T0 = 0x79CC4519
	sm3T1 = 0x7A879D8A
)

// sm3IV is the SM3 initial vector.
var sm3IV = [8]uint32{
	0x7380166F,
	0x4914B2B9,
	0x172442D7,
	0xDA8A0600,
	0xA96F30BC,
	0x163138AA,
	0xE38DEE4D,
	0xB0FB0E4E,
}

func sm3Rotl(value uint32, bits int) uint32 {
	bits &= 31
	return value<<bits | value>>((32-bits)&31)
}

func sm3FF(index int, x, y, z uint32) uint32 {
	if index < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (x & z) | (y & z)
}

func sm3GG(index int, x, y, z uint32) uint32 {
	if index < 16 {
		return x ^ y ^ z
	}
	return (x & y) | (^x & z)
}

func sm3Expand(block []byte) (w [68]uint32, w1 [64]uint32) {
	for i := 0; i < 16; i++ {
		w[i] = binary.BigEndian.Uint32(block[i*4:])
	}
	for i := 16; i < 68; i++ {
		x := w[i-16] ^ w[i-9] ^ sm3Rotl(w[i-3], 15)
		x = x ^ sm3Rotl(x, 15) ^ sm3Rotl(x, 23)
		w[i] = x ^ sm3Rotl(w[i-13], 7) ^ w[i-6]
	}
	for i := 0; i < 64; i++ {
		w1[i] = w[i] ^ w[i+4]
	}
	return w, w1
}

func sm3Compress(state [8]uint32, block []byte) [8]uint32 {
	w, w1 := sm3Expand(block)
	a, b, c, d := state[0], state[1], state[2], state[3]
	e, f, g, h := state[4], state[5], state[6], state[7]
	for j := 0; j < 64; j++ {
		t := uint32(sm3T0)
		if j >= 16 {
			t = sm3T1
		}
		ss1 := sm3Rotl(sm3Rotl(a, 12)+e+sm3Rotl(t, j), 7)
		ss2 := ss1 ^ sm3Rotl(a, 12)
		tt1 := sm3FF(j, a, b, c) + d + ss2 + w1[j]
		tt2 := sm3GG(j, e, f, g) + h + ss1 + w[j]
		d = c
		c = sm3Rotl(b, 9)
		b = a
		a = tt1
		h = g
		g = sm3Rotl(f, 19)
		f = e
		e = tt2 ^ sm3Rotl(tt2, 9) ^ sm3Rotl(tt2, 17)
	}
	return [8]uint32{
		state[0] ^ a,
		state[1] ^ b,
		state[2] ^ c,
		state[3] ^ d,
		state[4] ^ e,
		state[5] ^ f,
		state[6] ^ g,
		state[7] ^ h,
	}
}

// sm3Pad appends the mandatory 0x80 byte, zero padding and the 64-bit
// big-endian bit length, matching the Python implementation.
func sm3Pad(message []byte) []byte {
	bitLength := uint64(len(message)) * 8
	padded := make([]byte, 0, len(message)+72)
	padded = append(padded, message...)
	padded = append(padded, 0x80)
	for len(padded)%64 != 56 {
		padded = append(padded, 0x00)
	}
	return binary.BigEndian.AppendUint64(padded, bitLength)
}

// SM3Hash returns the 32 byte SM3 digest of msg.
func SM3Hash(msg []byte) [32]byte {
	state := sm3IV
	padded := sm3Pad(msg)
	for offset := 0; offset < len(padded); offset += 64 {
		state = sm3Compress(state, padded[offset:offset+64])
	}
	var out [32]byte
	for i, word := range state {
		binary.BigEndian.PutUint32(out[i*4:], word)
	}
	return out
}

// SM3HexDigest returns the lowercase hex form of SM3Hash.
func SM3HexDigest(msg []byte) string {
	sum := SM3Hash(msg)
	return hex.EncodeToString(sum[:])
}

// SM3ToArray returns the SM3 digest as 32 integers in the range 0..255, the
// shape consumed by the a_bogus port.
func SM3ToArray(msg []byte) []int {
	sum := SM3Hash(msg)
	out := make([]int, len(sum))
	for i, b := range sum {
		out[i] = int(b)
	}
	return out
}
