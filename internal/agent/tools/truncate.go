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

	// Walk back from the limit until we land on the start of a rune.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
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
