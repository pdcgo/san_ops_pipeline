package san_ops_pipeline

import (
	"strings"
	"unicode"
)

// screenText makes one line of a step's output safe to draw inside the live view: what a terminal
// would leave of a line rewritten with `\r` (a progress bar) — the text after the last `\r` —
// without escape codes (colours, cursor moves: a step's own `\x1b[2J` must not wreck the view),
// without other control characters, tabs turned into spaces up to the next multiple of 8.
func screenText(s string) string {
	i := strings.LastIndexByte(s, '\r')
	if i >= 0 {
		s = s[i+1:]
	}

	var b strings.Builder

	col := 0
	rs := []rune(s)

	for k := 0; k < len(rs); k++ {
		r := rs[k]

		switch {
		case r == '\t':
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case r == 0x1b:
			k = skipEscape(rs, k)
		case r < 0x20 || (r >= 0x7f && r < 0xa0):
			// another control character: dropped
		default:
			b.WriteRune(r)
			col += runeCells(r)
		}
	}

	return b.String()
}

// skipEscape returns the index of the last rune of the escape sequence that starts at rs[k] (an
// ESC): CSI `ESC [ … final`, OSC `ESC ] … BEL` or `… ESC \`, or ESC and one more rune.
func skipEscape(rs []rune, k int) int {
	if k+1 >= len(rs) {
		return k
	}

	switch rs[k+1] {
	case '[':
		for j := k + 2; j < len(rs); j++ {
			if rs[j] >= 0x40 && rs[j] <= 0x7e {
				return j
			}
		}

		return len(rs) - 1
	case ']':
		for j := k + 2; j < len(rs); j++ {
			if rs[j] == 0x07 {
				return j
			}

			if rs[j] == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' {
				return j + 1
			}
		}

		return len(rs) - 1
	default:
		return k + 1
	}
}

// cells is how many terminal columns s takes.
func cells(s string) int {
	n := 0
	for _, r := range s {
		n += runeCells(r)
	}

	return n
}

// cutCells cuts s to at most n columns, ending it with `…` when something was cut.
func cutCells(s string, n int) string {
	if n <= 0 {
		return ""
	}

	if cells(s) <= n {
		return s
	}

	var b strings.Builder

	used := 0

	for _, r := range s {
		w := runeCells(r)
		if used+w > n-1 {
			break
		}

		b.WriteRune(r)
		used += w
	}

	b.WriteString("…")

	return b.String()
}

// padCells pads s with spaces to n columns.
func padCells(s string, n int) string {
	c := cells(s)
	if c >= n {
		return s
	}

	return s + strings.Repeat(" ", n-c)
}

// runeCells is how many columns a terminal gives r: 0 for combining marks and format characters,
// 2 for the wide East Asian scripts and emoji, 1 for the rest. It is an approximation — terminals
// disagree at the edges — which is why the live view also keeps its last column free.
func runeCells(r rune) int {
	switch {
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return 0
	case r < 0x300:
		return 1
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}

// wideRanges are the code points terminals draw two columns wide (East Asian Wide and Fullwidth,
// and the emoji blocks), in order.
var wideRanges = [][2]rune{
	{0x1100, 0x115f}, {0x231a, 0x231b}, {0x2329, 0x232a}, {0x23e9, 0x23ec}, {0x23f0, 0x23f0},
	{0x23f3, 0x23f3}, {0x25fd, 0x25fe}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267f, 0x267f},
	{0x2693, 0x2693}, {0x26a1, 0x26a1}, {0x26aa, 0x26ab}, {0x26bd, 0x26be}, {0x26c4, 0x26c5},
	{0x26ce, 0x26ce}, {0x26d4, 0x26d4}, {0x26ea, 0x26ea}, {0x26f2, 0x26f3}, {0x26f5, 0x26f5},
	{0x26fa, 0x26fa}, {0x26fd, 0x26fd}, {0x2705, 0x2705}, {0x270a, 0x270b}, {0x2728, 0x2728},
	{0x274c, 0x274c}, {0x274e, 0x274e}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27b0, 0x27b0}, {0x27bf, 0x27bf}, {0x2b1b, 0x2b1c}, {0x2b50, 0x2b50}, {0x2b55, 0x2b55},
	{0x2e80, 0x303e}, {0x3041, 0x33ff}, {0x3400, 0x4dbf}, {0x4e00, 0x9fff}, {0xa000, 0xa4cf},
	{0xa960, 0xa97f}, {0xac00, 0xd7a3}, {0xf900, 0xfaff}, {0xfe10, 0xfe19}, {0xfe30, 0xfe6f},
	{0xff00, 0xff60}, {0xffe0, 0xffe6}, {0x1f300, 0x1f64f}, {0x1f680, 0x1f6ff}, {0x1f900, 0x1f9ff},
	{0x1fa70, 0x1faff}, {0x20000, 0x3fffd},
}

func isWide(r rune) bool {
	lo, hi := 0, len(wideRanges)
	for lo < hi {
		m := (lo + hi) / 2

		switch {
		case r < wideRanges[m][0]:
			hi = m
		case r > wideRanges[m][1]:
			lo = m + 1
		default:
			return true
		}
	}

	return false
}
