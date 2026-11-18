//go:build cgo

package spell

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	espell "github.com/ge-editor/editorleaf/spell"
)

// The engine that editorleaf/spell uses. Linking this package registers it
// (see ge/register_spell.go), like the tree-sitter parsers.
func init() {
	espell.Register(func(lang string) (espell.Engine, error) { return newEngine(lang) })
}

// cacheLimit is how many different words the engine remembers the answer for.
const cacheLimit = 50000

type engine struct {
	hs       *hunspell
	aff      string       // the affix file of the language
	extras   []*hunspell // the dictionaries of the language mode (LoadMode)
	lang     string
	personal string // the file of the personal dictionary
	cache    map[string]bool
}

func newEngine(lang string) (*engine, error) {
	aff, dic, err := findDictionary(lang)
	if err != nil {
		return nil, err
	}
	hs, err := openHunspell(aff, dic)
	if err != nil {
		return nil, err
	}
	e := &engine{
		hs:       hs,
		aff:      aff,
		lang:     lang,
		personal: personalDictionaryPath(lang),
		cache:    map[string]bool{},
	}
	e.loadPersonal()
	return e, nil
}

// Misspelled implements editorleaf/spell.Engine: the words of a text.
func (e *engine) Misspelled(text []byte) []espell.Range {
	return e.check(tokens(text))
}

// MisspelledCode implements editorleaf/spell.CodeEngine: the words of the
// identifiers of a program.
func (e *engine) MisspelledCode(text []byte) []espell.Range {
	return e.check(codeTokens(text))
}

// check looks the words up.
func (e *engine) check(toks []token) []espell.Range {
	var out []espell.Range
	for _, t := range toks {
		ok, known := e.cache[t.word]
		if !known {
			ok = e.spellWord(t.word)
			if len(e.cache) >= cacheLimit {
				e.cache = map[string]bool{}
			}
			e.cache[t.word] = ok
		}
		if !ok {
			out = append(out, espell.Range{Start: t.start, End: t.end})
		}
	}
	return out
}

// spellWord: the word is right in the dictionary of the language or in one of
// the language mode.
func (e *engine) spellWord(word string) bool {
	if e.hs.spell(word) {
		return true
	}
	for _, x := range e.extras {
		if x.spell(word) {
			return true
		}
	}
	return false
}

// LoadMode implements editorleaf/spell.ModeEngine: it adds the dictionaries
// named after a language mode (see dict.go). A dictionary that cannot be
// opened is not an error of the whole check: it is reported in warnings.
func (e *engine) LoadMode(mode string) (loaded, warnings []string) {
	for _, d := range modeDictionaries(mode, e.aff) {
		hs, err := openHunspell(d.aff, d.dic)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", d.name, err))
			continue
		}
		e.extras = append(e.extras, hs)
		if len(loaded) == 0 || loaded[len(loaded)-1] != d.name {
			loaded = append(loaded, d.name)
		}
	}
	e.cache = map[string]bool{} // what was misspelled may be right now
	return loaded, warnings
}

// Suggest implements editorleaf/spell.Engine. The suggestions come from the
// dictionary of the language only.
func (e *engine) Suggest(word string, n int) []string {
	s := e.hs.suggest(word)
	if n > 0 && len(s) > n {
		s = s[:n]
	}
	return s
}

// Add implements editorleaf/spell.Engine: the word is accepted now and
// appended to the personal dictionary file.
func (e *engine) Add(word string) error {
	word = strings.TrimSpace(word)
	if word == "" || strings.ContainsAny(word, "\r\n") {
		return fmt.Errorf("not a word: %q", word)
	}
	if e.personal != "" {
		if err := os.MkdirAll(filepath.Dir(e.personal), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(e.personal, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, werr := f.WriteString(word + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	e.Ignore(word)
	return nil
}

// Ignore implements editorleaf/spell.Engine.
func (e *engine) Ignore(word string) {
	e.hs.add(word)
	e.cache = map[string]bool{} // what was misspelled may be right now
}

// Close implements editorleaf/spell.Engine.
func (e *engine) Close() {
	e.hs.close()
	for _, x := range e.extras {
		x.close()
	}
	e.extras = nil
}

// loadPersonal adds the words of the personal dictionary (one per line; empty
// lines and lines that start with # are skipped).
func (e *engine) loadPersonal() {
	if e.personal == "" {
		return
	}
	f, err := os.Open(e.personal)
	if err != nil {
		return // there is none yet
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		w := strings.TrimSpace(sc.Text())
		if w != "" && !strings.HasPrefix(w, "#") {
			e.hs.add(w)
		}
	}
}
