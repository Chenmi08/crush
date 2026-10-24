package tools

import "unicode/utf8"

// truncateUTF8 returns at most maxBytes bytes of s without splitting a
// multi-byte UTF-8 rune. Slicing a string by byte offset can leave a
// dangling continuation byte, which then fails UTF-8 validation and
// renders as a replacement character downstream.
func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	return s[:runeStartAtOrBefore(s, maxBytes)]
}

// runeStartAtOrBefore returns the largest offset <= i that begins a rune,
// clamped to the length of s. Callers slicing s[:result] with it can never
// cut a multi-byte rune in half.
func runeStartAtOrBefore(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	if i < 0 {
		return 0
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// runeStartAtOrAfter returns the smallest offset >= i that begins a rune,
// clamped to the length of s. Callers slicing s[result:] with it can never
// start in the middle of a multi-byte rune.
func runeStartAtOrAfter(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// truncateRunes returns at most maxRunes runes of s.
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}

	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}
