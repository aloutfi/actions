package core

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"unicode/utf8"
)

// funcMap is every function a template can call: pure, no I/O, no access to
// the environment or files. Keep it this small. details depends on the host
// dialect, so it is bound per render (see funcsFor).
var funcMap = template.FuncMap{
	"fence":   fence,
	"html":    html.EscapeString,
	"code":    code,
	"plural":  plural,
	"details": func(open bool, summary, body string) string { return details(HostFor(""), open, summary, body) },
	"short":   short,
}

// funcsFor binds details to host.
func funcsFor(host Host) template.FuncMap {
	return template.FuncMap{
		"details": func(open bool, summary, body string) string { return details(host, open, summary, body) },
	}
}

// fence wraps text in a fenced code block whose backtick run is longer than
// any run inside text, so nothing in text can close it.
func fence(lang, text string) string {
	d := strings.Repeat("`", max(3, longestRun(text, '`')+1))
	return d + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + d
}

// code is an inline code span that text cannot close: its delimiter is longer
// than any backtick run in s.
func code(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	d := strings.Repeat("`", longestRun(s, '`')+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return d + s + d
}

func longestRun(s string, c rune) int {
	best, n := 0, 0
	for _, r := range s {
		if r == c {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
}

// plural is "1 resource" or "3,000 resources".
func plural(n any, word string) string {
	var v int
	switch x := n.(type) {
	case int:
		v = x
	case Num:
		v = int(x)
	default:
		return fmt.Sprintf("%v %ss", n, word)
	}
	if v == 1 {
		return "1 " + word
	}
	return Num(v).String() + " " + word + "s"
}

// details is a collapsible block where the host renders one, else a heading
// and the body. summary is plain text; it is escaped here.
func details(host Host, open bool, summary, body string) string {
	if !host.Details {
		return "#### " + html.EscapeString(summary) + "\n\n" + body
	}
	attr := ""
	if open {
		attr = " open"
	}
	return "<details" + attr + "><summary>" + html.EscapeString(summary) + "</summary>\n\n" + body + "\n\n</details>"
}

// short is the first 7 characters of a commit id.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Num is a count that prints with thousands separators: 58,210.
type Num int

func (n Num) String() string {
	s := strconv.Itoa(int(n))
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// unchanged matches terraform's "# (N unchanged attributes hidden)" lines.
var unchanged = regexp.MustCompile(`^\s*# \(\d+ unchanged (attribute|block|element)s? hidden\)\s*$`)

// dropUnchanged removes the "unchanged ... hidden" lines: noise in a comment.
func dropUnchanged(text string) string {
	lines := strings.SplitAfter(text, "\n")
	out := lines[:0]
	for _, l := range lines {
		if !unchanged.MatchString(l) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "")
}

// measure is how a budget counts text: characters (GitHub comments) or
// bytes (job summary files).
type measure int

const (
	inRunes measure = iota
	inBytes
)

func (m measure) of(s string) int {
	if m == inBytes {
		return len(s)
	}
	return utf8.RuneCountInString(s)
}

func (m measure) String() string {
	if m == inBytes {
		return "bytes"
	}
	return "characters"
}

// cut shortens text to at most limit, at a line boundary when it can and
// never inside a UTF-8 sequence.
func (m measure) cut(text string, limit int) string {
	if m.of(text) <= limit {
		return text
	}
	used, end := 0, 0
	for _, line := range strings.SplitAfter(text, "\n") {
		n := m.of(line)
		if used+n > limit {
			break
		}
		used += n
		end += len(line)
	}
	if end == 0 { // the first line alone is too long
		for i, r := range text {
			n := 1
			if m == inBytes {
				n = utf8.RuneLen(r)
			}
			if used+n > limit {
				return text[:i]
			}
			used += n
		}
	}
	return strings.TrimRight(text[:end], "\n")
}
