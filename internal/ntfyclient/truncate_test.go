package ntfyclient_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/felipeelias/claude-notifier/internal/ntfyclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chineseCodePoint is the code point of a single Han character (U+4E2D).
const chineseCodePoint = 0x4E2D

// chineseChar is that character as a one-character string, derived from its
// code point so this file stays pure ASCII (Han-script string literals are
// flagged by gosmopolitan); the runtime value is identical.
var chineseChar = string(rune(chineseCodePoint))

func TestTruncateRunes_ShortStringUnchanged(t *testing.T) {
	assert.Equal(t, "hello", ntfyclient.TruncateRunes("hello", 50))
}

func TestTruncateRunes_AsciiTruncationAppendsEllipsis(t *testing.T) {
	in := strings.Repeat("a", 100)
	got := ntfyclient.TruncateRunes(in, 50)
	require.Len(t, got, 53) // 50 runes + "..."
	require.True(t, strings.HasPrefix(got, strings.Repeat("a", 50)))
	require.True(t, strings.HasSuffix(got, "..."))
}

func TestTruncateRunes_ChineseProducesValidUTF8(t *testing.T) {
	// Each Chinese character is 3 bytes in UTF-8. The old byte-based
	// truncation `s[:50]` would slice through the middle of a character
	// and produce invalid UTF-8. Rune-based truncation must not.
	in := strings.Repeat(chineseChar, 100) // 300 bytes
	got := ntfyclient.TruncateRunes(in, 50)

	require.True(t, utf8.ValidString(got), "truncated string must be valid UTF-8")

	// 50 Chinese runes exactly (no "..."), since the rune count equals the cap
	// only when len > max — here len(runes)=100 > 50, so it truncates and adds "..."
	runes := []rune(got)
	require.Len(t, runes, 53, "expected 50 truncated runes + 3 ellipsis runes")
	require.True(t, strings.HasPrefix(got, strings.Repeat(chineseChar, 50)))
	require.True(t, strings.HasSuffix(got, "..."))

	// Byte length: 50 * 3 + 3 = 153. Must NOT be 50 bytes (which would imply
	// byte slicing and would have produced invalid UTF-8).
	require.Len(t, got, 153)
}

func TestTruncateRunes_ExactlyAtLimitNoTruncation(t *testing.T) {
	in := strings.Repeat("a", 50)
	assert.Equal(t, in, ntfyclient.TruncateRunes(in, 50))
}

func TestTruncateRunesNoEllipsis_ChineseProducesValidUTF8(t *testing.T) {
	in := strings.Repeat(chineseChar, 100)
	got := ntfyclient.TruncateRunesNoEllipsis(in, 50)

	require.True(t, utf8.ValidString(got))
	runes := []rune(got)
	require.Len(t, runes, 50)
	require.Equal(t, strings.Repeat(chineseChar, 50), got)
}

func TestStripMarkdown_LongChineseProducesValidUTF8(t *testing.T) {
	// Build a >4000-rune Chinese input and ensure the output stays valid
	// UTF-8 and is capped to 4000 runes (no invalid boundary).
	in := strings.Repeat(chineseChar, 5000)
	got := ntfyclient.StripMarkdown(in)

	require.True(t, utf8.ValidString(got), "StripMarkdown output must be valid UTF-8")
	assert.Equal(t, strings.Repeat(chineseChar, 4000), got, "should cap at 4000 runes exactly")
}
