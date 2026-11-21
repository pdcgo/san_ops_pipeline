package san_ops_pipeline

import (
	"fmt"
	"strconv"
	"strings"
)

// vtScreen is a fake terminal for the tests: it reads what the live view writes — text, `\n`,
// `\r`, and the escape codes the view uses (`ESC[nA` cursor up, `ESC[2K` erase the line, `ESC[J`
// erase below) — into a screen of width columns, so a test can compare the screen with a golden
// text. A character written past the last column is a wrap: recorded, the test fails on it. An
// escape code the view should never send is recorded too. With a height, the screen scrolls like a
// real one: the cursor cannot go back above the visible top, so a frame taller than the screen
// leaves its old top behind.
type vtScreen struct {
	width   int
	height  int // 0: as tall as needed
	rows    [][]rune
	row     int
	col     int
	top     int // the first visible row
	wraps   []string
	unknown []string
	writes  int
	frames  []string // the screen after each write
}

func newVTScreen(width int) *vtScreen {
	return &vtScreen{width: width, rows: [][]rune{nil}}
}

func (s *vtScreen) Write(p []byte) (int, error) {
	s.writes++

	rs := []rune(string(p))

	for i := 0; i < len(rs); i++ {
		r := rs[i]

		switch {
		case r == '\n':
			s.row++
			s.col = 0
			s.grow()

			if s.height > 0 && s.row >= s.top+s.height {
				s.top = s.row - s.height + 1
			}
		case r == '\r':
			s.col = 0
		case r == 0x1b:
			i = s.escape(rs, i)
		default:
			if s.col == s.width { // the first character past the last column: one record per line
				s.wraps = append(s.wraps, fmt.Sprintf("row %d: %q", s.row, string(s.rows[s.row])))
			}

			line := s.rows[s.row]
			for len(line) <= s.col {
				line = append(line, ' ')
			}

			line[s.col] = r
			s.rows[s.row] = line
			s.col++
		}
	}

	s.frames = append(s.frames, s.screen())

	return len(p), nil
}

func (s *vtScreen) grow() {
	for len(s.rows) <= s.row {
		s.rows = append(s.rows, nil)
	}
}

// escape applies the CSI sequence starting at rs[i] and returns the index of its last rune.
func (s *vtScreen) escape(rs []rune, i int) int {
	if i+1 >= len(rs) || rs[i+1] != '[' {
		s.unknown = append(s.unknown, "ESC without [")

		return i
	}

	j := i + 2
	for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
		j++
	}

	if j >= len(rs) {
		s.unknown = append(s.unknown, "an unfinished escape code")

		return len(rs) - 1
	}

	arg := string(rs[i+2 : j])

	switch rs[j] {
	case 'A':
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 {
			s.unknown = append(s.unknown, "ESC["+arg+"A")
		}

		s.row = max(s.top, s.row-n)
	case 'K':
		if arg != "2" {
			s.unknown = append(s.unknown, "ESC["+arg+"K")
		}

		s.rows[s.row] = nil
	case 'J':
		if arg != "" && arg != "0" {
			s.unknown = append(s.unknown, "ESC["+arg+"J")
		}

		line := s.rows[s.row]
		if len(line) > s.col {
			s.rows[s.row] = line[:s.col]
		}

		for k := s.row + 1; k < len(s.rows); k++ {
			s.rows[k] = nil
		}
	default:
		s.unknown = append(s.unknown, "ESC["+arg+string(rs[j]))
	}

	return j
}

// screen is what the terminal shows: its rows, trailing spaces and empty rows at the end left out.
func (s *vtScreen) screen() string {
	lines := make([]string, len(s.rows))
	for i, r := range s.rows {
		lines[i] = strings.TrimRight(string(r), " ")
	}

	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) == 0 {
		return ""
	}

	return strings.Join(lines, "\n") + "\n"
}
