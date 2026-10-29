package model

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxText is the longest model, session or project string kept. Real values
// are a few dozen bytes; anything longer is a bug or hostile and would only
// inflate memory, the cache and the exports.
const MaxText = 1024

// Clean makes a string read from a log safe to print: control characters
// (C0, DEL, C1: the terminal escape sequences that retitle a window, clear
// the screen or write to the clipboard) and invisible direction overrides are
// removed, invalid UTF-8 is replaced and the length is bounded.
// Line breaks and tabs become spaces. Clean strings are returned unchanged.
func Clean(s string) string {
	if len(s) <= MaxText && !needsClean(s) {
		return s
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	var b strings.Builder
	b.Grow(min(len(s), MaxText) + 3)
	over := false
	for _, r := range s {
		if b.Len() >= MaxText {
			over = true
			break
		}
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case unsafeRune(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if over || len(out) > MaxText {
		cut := MaxText - 3
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "…"
	}
	return out
}

func needsClean(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || c >= 0x80 {
			if c < 0x80 {
				return true
			}
			// multibyte: decode and test
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n <= 1 || unsafeRune(r) {
				return true
			}
			i += n - 1
		}
	}
	return false
}

// unsafeRune: C0/C1 controls, DEL, and Unicode format characters (bidi
// overrides, zero-width joiners used to spoof text, the BOM).
func unsafeRune(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r < 0xa0) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029
}

// BoundKey keeps a dedupe key within a sane size without changing short keys.
func BoundKey(k string) string {
	if len(k) <= 512 {
		return k
	}
	var h uint64 = 14695981039346656037 // FNV-1a over the whole key
	for i := 0; i < len(k); i++ {
		h = (h ^ uint64(k[i])) * 1099511628211
	}
	cut := 480
	for cut > 0 && !utf8.RuneStart(k[cut]) {
		cut--
	}
	const hex = "0123456789abcdef"
	var t [16]byte
	for i := 15; i >= 0; i-- {
		t[i] = hex[h&15]
		h >>= 4
	}
	return k[:cut] + "#" + string(t[:])
}
