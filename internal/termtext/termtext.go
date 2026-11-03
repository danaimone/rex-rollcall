package termtext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func RuneWidth(r rune) int {
	switch {
	case r == 0x200d || r >= 0xfe00 && r <= 0xfe0f || r >= 0xe0100 && r <= 0xe01ef:
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case r < 0x1100:
		return 1
	case r <= 0x115f, r == 0x2329, r == 0x232a,
		r >= 0x231a && r <= 0x231b, r >= 0x23e9 && r <= 0x23ec, r == 0x23f0, r == 0x23f3,
		r >= 0x25fd && r <= 0x25fe, r >= 0x2614 && r <= 0x2615, r >= 0x2648 && r <= 0x2653,
		r == 0x267f, r == 0x2693, r == 0x26a1, r >= 0x26aa && r <= 0x26ab, r >= 0x26bd && r <= 0x26be,
		r >= 0x26c4 && r <= 0x26c5, r == 0x26ce, r == 0x26d4, r == 0x26ea, r >= 0x26f2 && r <= 0x26f3,
		r == 0x26f5, r == 0x26fa, r == 0x26fd, r == 0x2705, r >= 0x270a && r <= 0x270b, r == 0x2728,
		r == 0x274c, r == 0x274e, r >= 0x2753 && r <= 0x2755, r == 0x2757, r >= 0x2795 && r <= 0x2797,
		r == 0x27b0, r == 0x27bf, r >= 0x2b1b && r <= 0x2b1c, r == 0x2b50, r == 0x2b55,
		r >= 0x2e80 && r <= 0x303e, r >= 0x3041 && r <= 0x33ff, r >= 0x3400 && r <= 0x4dbf,
		r >= 0x4e00 && r <= 0x9fff, r >= 0xa000 && r <= 0xa4cf, r >= 0xa960 && r <= 0xa97f,
		r >= 0xac00 && r <= 0xd7a3, r >= 0xf900 && r <= 0xfaff, r >= 0xfe10 && r <= 0xfe19,
		r >= 0xfe30 && r <= 0xfe6f, r >= 0xff00 && r <= 0xff60, r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f004 && r <= 0x1f0cf, r >= 0x1f18e && r <= 0x1f19a, r >= 0x1f200 && r <= 0x1f251,
		r >= 0x1f300 && r <= 0x1f64f, r >= 0x1f680 && r <= 0x1f6ff, r >= 0x1f7e0 && r <= 0x1f7eb,
		r >= 0x1f90c && r <= 0x1f9ff, r >= 0x1fa70 && r <= 0x1faff, r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

func Width(s string) int {
	w := 0
	for _, r := range s {
		w += RuneWidth(r)
	}
	return w
}

func Truncate(s string, width int) string {
	if Width(s) <= width {
		return s
	}
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := RuneWidth(r)
		if w+rw > width-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return strings.TrimRight(b.String(), " ") + "…"
}

func Pad(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-Width(s)))
}

func PadLeft(s string, width int) string {
	return strings.Repeat(" ", max(0, width-Width(s))) + s
}

func Wrap(line string, width int) []string {
	line = strings.TrimRight(line, " ")
	if width < 8 || Width(line) <= width {
		return []string{line}
	}
	words := strings.TrimLeft(line, " ")
	cur := line[:len(line)-len(words)]
	indent := hangingIndent(line)
	if Width(indent) > width/2 {
		cur, indent = "", ""
	}
	var out []string
	empty := true
	for _, word := range strings.Fields(words) {
		for word != "" {
			room := width - Width(cur)
			if !empty {
				room--
			}
			if Width(word) <= room {
				if !empty {
					cur += " "
				}
				cur, word, empty = cur+word, "", false
				continue
			}
			if empty {
				var head string
				head, word = split(word, room)
				out = append(out, cur+head)
			} else {
				out = append(out, cur)
			}
			cur, empty = indent, true
		}
	}
	if !empty {
		out = append(out, cur)
	}
	return out
}

func split(word string, width int) (string, string) {
	w := 0
	for i, r := range word {
		rw := RuneWidth(r)
		if w+rw > width {
			if i == 0 {
				_, n := utf8.DecodeRuneInString(word)
				return word[:n], word[n:]
			}
			return word[:i], word[i:]
		}
		w += rw
	}
	return word, ""
}

func hangingIndent(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	lead := line[:len(line)-len(trimmed)]
	for _, bullet := range []string{"- ", "* ", "• ", "› "} {
		if strings.HasPrefix(trimmed, bullet) {
			return lead + strings.Repeat(" ", Width(bullet))
		}
	}
	if i := strings.Index(trimmed, ". "); i > 0 && i <= 3 && strings.Trim(trimmed[:i], "0123456789") == "" {
		return lead + strings.Repeat(" ", i+2)
	}
	return lead
}
