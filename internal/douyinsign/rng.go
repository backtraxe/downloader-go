// Random source for a_bogus noise.
//
// Ported from Evil0ctal/Douyin_TikTok_Download_API (Apache-2.0); the original
// copyright and license notice is retained. See LICENSE.apache in this
// directory.
// Upstream: src/dtk/signing/native/abogus.py @ main (pulled 2026-10-14)
// https://github.com/Evil0ctal/Douyin_TikTok_Download_API

package douyinsign

import "math/rand/v2"

// RNG is the one operation the generator needs from its random source. Tests
// inject a fixed sequence; production uses the package-level source.
type RNG interface {
	Float64() float64
}

// mathRNG adapts math/rand/v2's package-level Float64 to RNG.
type mathRNG struct{}

func (mathRNG) Float64() float64 { return rand.Float64() }
