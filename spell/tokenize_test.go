package spell

import (
	"slices"
	"strings"
	"testing"
)

func words(s string) []string {
	var out []string
	for _, t := range tokens([]byte(s)) {
		out = append(out, t.word)
	}
	return out
}

func TestTokens(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"The quick brown fox", []string{"The", "quick", "brown", "fox"}},
		{"it is a cat", []string{"cat"}}, // fewer than 3 letters
		{"don't stop, it's fine.", []string{"don't", "stop", "it's", "fine"}},
		{"“quoted” 'single'", []string{"quoted", "single"}},
		{"well-known state-of-the-art", []string{"well", "known", "state", "the", "art"}},

		// not words
		{"see https://example.com/a_b and www.example.org", []string{"see", "and"}},
		{"mail me@example.com now", []string{"mail", "now"}},
		{"open /usr/share/hunspell or ~/notes.txt", []string{"open"}},
		{"import github.com/ge-editor/gecore and/or yes/no", []string{"import"}},
		{"foo_bar abc123 0xDEADBEEF v1.2.3", nil},
		{"HTTP README NASA", nil}, // acronyms

		// camelCase
		{"camelCase", []string{"camel", "Case"}},
		{"HTTPServer", []string{"Server"}},
		{"parseURLNow", []string{"parse", "Now"}},

		// Japanese and Latin
		{"日本語 text 日本語", []string{"text"}},
		{"日本語text", nil}, // stuck to Japanese: skipped as a whole
		{"résumé naïve", []string{"résumé", "naïve"}},
	} {
		if got := words(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTokenRangesAreByteOffsets(t *testing.T) {
	text := []byte("日本語 hello  wörld")
	var got []string
	for _, tk := range tokens(text) {
		got = append(got, string(text[tk.start:tk.end]))
	}
	if !slices.Equal(got, []string{"hello", "wörld"}) {
		t.Fatalf("%q", got)
	}
}

func TestTypographicApostropheIsNormalized(t *testing.T) {
	text := []byte("don’t")
	tk := tokens(text)
	if len(tk) != 1 || tk[0].word != "don't" || string(text[tk[0].start:tk[0].end]) != "don’t" {
		t.Fatalf("%+v", tk)
	}
}

func codeWords(s string) []string {
	var out []string
	for _, t := range codeTokens([]byte(s)) {
		out = append(out, t.word)
	}
	return out
}

func TestCodeTokens(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"recieveData := parseURLNow(x)", []string{"recieve", "Data", "parse"}},
		{"user_name_list user2name", []string{"user", "name", "list", "user", "name"}},
		{"HTTPServer", []string{"Server"}},

		// not checked
		{"ctx err buf tmp idx", nil},                    // 3 letters or fewer
		{"strconv.Atoi(fmt.Sprintf(args))", nil},       // known terms
		{"0xDEADBEEF x1 abc123def", nil},               // hex, numbers
		{"MAX_SIZE HTTP", nil},                         // acronyms
		{"日本語var := 1", nil},                           // another script
		{"func (e *Editor) Recieve() error", []string{"Editor", "Recieve", "error"}},
	} {
		if got := codeWords(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCodeTokenRangesAreByteOffsets(t *testing.T) {
	text := []byte("  naïve_wörld := 1")
	var got []string
	for _, tk := range codeTokens(text) {
		got = append(got, string(text[tk.start:tk.end]))
	}
	if !slices.Equal(got, []string{"naïve", "wörld"}) {
		t.Fatalf("%q", got)
	}
}

func TestProgrammingTermsAreLowerCase(t *testing.T) {
	for w := range programmingTerms {
		if w != strings.ToLower(w) || strings.ContainsAny(w, " \t\n") {
			t.Errorf("%q", w)
		}
	}
	for _, w := range []string{"strconv", "goroutine", "printf", "args"} {
		if _, ok := programmingTerms[w]; !ok {
			t.Errorf("%q is missing", w)
		}
	}
}
