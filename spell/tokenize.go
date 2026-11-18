package spell

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// token is a word to look up: its byte range in the text, and the word
// itself (with the typographic apostrophe changed to ').
type token struct {
	start, end int
	word       string
}

// tokens finds the words of text that are worth checking.
//
// What is not checked: URLs, e-mail addresses and paths; a run of letters
// with a digit or "_" in it (identifiers, hex, versions); a run with a letter
// of another script than Latin (Japanese text and the words stuck to it);
// words of fewer than 3 letters; words in capitals only (acronyms). A
// camelCase or HTTPServer run is checked as its parts: camel, Case, HTTP
// (skipped), Server. A hyphen separates words.
func tokens(text []byte) []token {
	var out []token
	i := 0
	for i < len(text) {
		for i < len(text) && isSpace(text[i]) {
			i++
		}
		fs := i
		for i < len(text) && !isSpace(text[i]) {
			i++
		}
		if fs == i {
			break
		}
		if skipField(text[fs:i]) {
			continue
		}
		out = scanField(out, text, fs, i)
	}
	return out
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' }

// skipField reports whether a piece of text between spaces is a URL, an
// e-mail address or a path (anything with a "/" in it), which are not words.
func skipField(f []byte) bool {
	s := string(f)
	switch {
	case strings.Contains(s, "://"), strings.HasPrefix(s, "www."):
		return true
	case strings.Contains(s, "/"):
		// a path or an import path (/usr/share, ~/notes, github.com/a/b);
		// "and/or" is lost with them
		return true
	}
	if at := strings.IndexByte(s, '@'); at > 0 && strings.IndexByte(s[at:], '.') > 0 {
		return true
	}
	return false
}

// runeAt is a rune of a run, with its place in the text.
type runeAt struct {
	r         rune
	off, size int
}

// scanField collects the runs of letters (and digits, "_", and an apostrophe
// between letters) of text[fs:fe].
func scanField(out []token, text []byte, fs, fe int) []token {
	var run []runeAt
	flush := func() {
		if len(run) > 0 {
			out = emitRun(out, run)
			run = run[:0]
		}
	}
	for p := fs; p < fe; {
		r, size := utf8.DecodeRune(text[p:fe])
		word := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
		if !word && (r == '\'' || r == '’') && len(run) > 0 && unicode.IsLetter(run[len(run)-1].r) && p+size < fe {
			if next, _ := utf8.DecodeRune(text[p+size : fe]); unicode.IsLetter(next) {
				word = true
			}
		}
		if word {
			run = append(run, runeAt{r: r, off: p, size: size})
		} else {
			flush()
		}
		p += size
	}
	flush()
	return out
}

// emitRun turns a run into tokens, splitting camelCase.
func emitRun(out []token, run []runeAt) []token {
	for _, x := range run {
		if x.r == '_' || unicode.IsDigit(x.r) {
			return out
		}
		if unicode.IsLetter(x.r) && !unicode.Is(unicode.Latin, x.r) {
			return out
		}
	}
	start := 0
	for i := 1; i <= len(run); i++ {
		if i == len(run) || camelBoundary(run, i) {
			out = emitPart(out, run[start:i])
			start = i
		}
	}
	return out
}

// camelBoundary reports whether a new word starts at run[i]: a capital after
// a small letter (camel|Case), or the last capital of a row of capitals that
// is followed by a small letter (HTTP|Server).
func camelBoundary(run []runeAt, i int) bool {
	prev, cur := run[i-1].r, run[i].r
	if unicode.IsLower(prev) && unicode.IsUpper(cur) {
		return true
	}
	return unicode.IsUpper(prev) && unicode.IsUpper(cur) && i+1 < len(run) && unicode.IsLower(run[i+1].r)
}

func emitPart(out []token, part []runeAt) []token {
	letters, lower := 0, 0
	for _, x := range part {
		if unicode.IsLetter(x.r) {
			letters++
			if unicode.IsLower(x.r) {
				lower++
			}
		}
	}
	if letters < 3 || lower == 0 {
		return out // too short, or an acronym
	}
	word := make([]rune, len(part))
	for i, x := range part {
		word[i] = x.r
		if x.r == '’' {
			word[i] = '\''
		}
	}
	last := part[len(part)-1]
	return append(out, token{
		start: part[0].off,
		end:   last.off + last.size,
		word:  string(word),
	})
}

// ---------------------------------------------------------------------------
// Code
// ---------------------------------------------------------------------------

// codeMinLetters: a shorter word of an identifier is not checked. Names are
// full of abbreviations of 3 letters or fewer (ctx, err, buf, tmp, idx), and
// a misspelling that short is rare.
const codeMinLetters = 4

// codeTokens finds the words in code, which is what is left of a row when the
// comments and strings are taken out (MaskCode). An identifier is split into
// its words at "_", at digits and at capitals: user_name, user2name and
// userName are user and name. Not checked: a word of fewer than
// codeMinLetters letters, a word in capitals (an acronym), a word of the
// programmingTerms, a hexadecimal number, and an identifier that has a letter
// of another script than Latin.
func codeTokens(text []byte) []token {
	var out []token
	var run []runeAt
	flush := func() {
		if len(run) > 0 {
			out = emitCodeRun(out, run)
			run = run[:0]
		}
	}
	for p := 0; p < len(text); {
		r, size := utf8.DecodeRune(text[p:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			run = append(run, runeAt{r: r, off: p, size: size})
		} else {
			flush()
		}
		p += size
	}
	flush()
	return out
}

// emitCodeRun splits a run of letters, digits and "_" into words.
func emitCodeRun(out []token, run []runeAt) []token {
	if len(run) >= 2 && run[0].r == '0' && (run[1].r == 'x' || run[1].r == 'X') {
		return out // 0xDEADBEEF
	}
	for _, x := range run {
		if unicode.IsLetter(x.r) && !unicode.Is(unicode.Latin, x.r) {
			return out
		}
	}

	start := -1 // the start of the group of letters that is open
	for i := 0; i <= len(run); i++ {
		letter := i < len(run) && unicode.IsLetter(run[i].r)
		switch {
		case letter && start < 0:
			start = i
		case !letter && start >= 0:
			out = emitCodeGroup(out, run[start:i])
			start = -1
		}
	}
	return out
}

// emitCodeGroup splits a group of letters at the capitals (camelCase).
func emitCodeGroup(out []token, group []runeAt) []token {
	start := 0
	for i := 1; i <= len(group); i++ {
		if i == len(group) || camelBoundary(group, i) {
			out = emitCodePart(out, group[start:i])
			start = i
		}
	}
	return out
}

func emitCodePart(out []token, part []runeAt) []token {
	if len(part) < codeMinLetters {
		return out
	}
	lower := 0
	word := make([]rune, len(part))
	for i, x := range part {
		word[i] = x.r
		if unicode.IsLower(x.r) {
			lower++
		}
	}
	if lower == 0 {
		return out // an acronym
	}
	w := string(word)
	if _, known := programmingTerms[strings.ToLower(w)]; known {
		return out
	}
	last := part[len(part)-1]
	return append(out, token{start: part[0].off, end: last.off + last.size, word: w})
}
