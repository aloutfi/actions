package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFence(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"plain", "```hcl\nplain\n```"},
		{"a ``` b", "````hcl\na ``` b\n````"},
		{"`````\n", "``````hcl\n`````\n``````"},
	} {
		if got := fence("hcl", c.text); got != c.want {
			t.Errorf("fence(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestCode(t *testing.T) {
	for in, want := range map[string]string{
		"infra":   "`infra`",
		"a`b":     "``a`b``",
		"`edge":   "`` `edge ``",
		"two\nln": "`two ln`",
		"a<b>":    "`a<b>`",
	} {
		if got := code(in); got != want {
			t.Errorf("code(%q) = %q, want %q", in, got, want)
		}
	}
}

// details escapes its summary in both dialects.
func TestDetails(t *testing.T) {
	got := details(Host{Details: true}, false, `a<b>&"c"`, "body")
	if want := "<details><summary>a&lt;b&gt;&amp;&#34;c&#34;</summary>\n\nbody\n\n</details>"; got != want {
		t.Errorf("got %q", got)
	}
	if got := details(Host{}, true, "<x>", "body"); got != "#### &lt;x&gt;\n\nbody" {
		t.Errorf("no-details dialect: %q", got)
	}
	if got := details(Host{Details: true}, true, "s", "b"); got != "<details open><summary>s</summary>\n\nb\n\n</details>" {
		t.Errorf("open: %q", got)
	}
}

func TestPluralAndNum(t *testing.T) {
	for _, c := range []struct {
		n    any
		want string
	}{{1, "1 line"}, {0, "0 lines"}, {Num(58210), "58,210 lines"}, {1234567, "1,234,567 lines"}} {
		if got := plural(c.n, "line"); got != c.want {
			t.Errorf("plural(%v) = %q, want %q", c.n, got, c.want)
		}
	}
	if Num(-1234).String() != "-1,234" || Num(999).String() != "999" {
		t.Errorf("Num: %s %s", Num(-1234), Num(999))
	}
}

// Truncation never splits a UTF-8 sequence, and prefers line boundaries.
func TestCutIsUTF8Safe(t *testing.T) {
	text := "ab\nçé€😀\nlast"
	for limit := 0; limit <= len(text); limit++ {
		for _, m := range []measure{inRunes, inBytes} {
			got := m.cut(text, limit)
			if !utf8.ValidString(got) || m.of(got) > limit || !strings.HasPrefix(text, got) {
				t.Errorf("%v cut(%d) = %q", m, limit, got)
			}
		}
	}
	if got := inRunes.cut(text, 8); got != "ab\nçé€😀" {
		t.Errorf("line boundary: %q", got)
	}
	if got := inBytes.cut("😀😀", 5); got != "😀" {
		t.Errorf("bytes inside a rune: %q", got)
	}
}

func TestDropUnchanged(t *testing.T) {
	in := "a\n        # (1 unchanged attribute hidden)\n    # (12 unchanged blocks hidden)\n  # (3 unchanged elements hidden)\nkeep # (2 unchanged attributes hidden)\n# (because x)\n"
	if got, want := dropUnchanged(in), "a\nkeep # (2 unchanged attributes hidden)\n# (because x)\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHostFor(t *testing.T) {
	if h := HostFor("github"); h.MaxComment != 65536 || h.MaxSummary != 1<<20 || !h.Details || !h.DiffFence {
		t.Errorf("github: %+v", h)
	}
	if h := HostFor("azure"); h.MaxComment != 140000 || h.MaxSummary != 512<<10 || !h.Details || !h.DiffFence || h.HiddenHTML || h.SummaryDetails {
		t.Errorf("azure: %+v", h)
	}
	if HostFor("nonesuch") != HostFor("github") {
		t.Errorf("unknown hosts get the default row")
	}
}

// Blank lines before an over-long line must not leave an empty cut: the
// text falls back to a character cut instead of disappearing.
func TestCutAfterBlankLines(t *testing.T) {
	text := "\n\n" + strings.Repeat("x", 50) + "\nnext"
	for _, m := range []measure{inRunes, inBytes} {
		got := m.cut(text, 10)
		if strings.TrimSpace(got) == "" || m.of(got) > 10 || !strings.HasPrefix(text, got) {
			t.Errorf("%v: cut = %q", m, got)
		}
	}
}
