package spell

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ge-editor/utils"
)

// Where the dictionary of a language (Hunspell's <lang>.aff and <lang>.dic)
// is looked for, in this order:
//
//  1. $GE_SPELL_DICT_DIR
//  2. the user's: <user config dir>/ge/spell/
//  3. the system's: /usr/share/hunspell, /usr/share/myspell, ...
//  4. the one built into ge (the dict directory of this package), which is
//     written to <user cache dir>/ge/spell/ first because libhunspell opens
//     files, not memory.

//go:embed dict
var embedded embed.FS

// systemDictDirs are the directories where Linux distributions, Homebrew and
// macOS put Hunspell dictionaries.
func systemDictDirs() []string {
	dirs := []string{
		"/usr/share/hunspell",
		"/usr/share/myspell",
		"/usr/share/myspell/dicts",
		"/usr/local/share/hunspell",
		"/opt/homebrew/share/hunspell",
		"/Library/Spelling",
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Library", "Spelling"))
	}
	return dirs
}

// userDictDir is the directory of the user's own dictionaries and of the
// personal word lists.
func userDictDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, utils.GetExecutableBaseName(), "spell")
}

// personalDictionaryPath is the file the words that the user added go to.
func personalDictionaryPath(lang string) string {
	dir := userDictDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "personal_"+lang+".txt")
}

func hasDictionary(dir, lang string) (aff, dic string, ok bool) {
	if dir == "" {
		return "", "", false
	}
	aff = filepath.Join(dir, lang+".aff")
	dic = filepath.Join(dir, lang+".dic")
	for _, p := range []string{aff, dic} {
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return "", "", false
		}
	}
	return aff, dic, true
}

// findDictionary returns the paths of the .aff and .dic files of lang.
func findDictionary(lang string) (aff, dic string, err error) {
	if lang == "" || filepath.Base(lang) != lang {
		return "", "", fmt.Errorf("bad language name %q", lang)
	}

	dirs := []string{}
	if d := os.Getenv("GE_SPELL_DICT_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, userDictDir())
	dirs = append(dirs, systemDictDirs()...)
	for _, dir := range dirs {
		if aff, dic, ok := hasDictionary(dir, lang); ok {
			return aff, dic, nil
		}
	}

	if aff, dic, err := extractEmbedded(lang); err == nil {
		return aff, dic, nil
	}
	return "", "", fmt.Errorf("no dictionary for %s: put %s.aff and %s.dic in %s",
		lang, lang, lang, searchHint())
}

func searchHint() string {
	if d := userDictDir(); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		return `%AppData%\ge\spell`
	}
	return "~/.config/ge/spell"
}

// extractEmbedded writes the built-in dictionary of lang to the cache
// directory (once: the directory is named after the content) and returns the
// paths.
func extractEmbedded(lang string) (aff, dic string, err error) {
	affData, err := embedded.ReadFile("dict/" + lang + ".aff")
	if err != nil {
		return "", "", err
	}
	dicData, err := embedded.ReadFile("dict/" + lang + ".dic")
	if err != nil {
		return "", "", err
	}

	cache, err := os.UserCacheDir()
	if err != nil {
		return "", "", err
	}
	sum := sha256.New()
	sum.Write(affData)
	sum.Write(dicData)
	dir := filepath.Join(cache, utils.GetExecutableBaseName(), "spell", hex.EncodeToString(sum.Sum(nil))[:16])

	aff = filepath.Join(dir, lang+".aff")
	dic = filepath.Join(dir, lang+".dic")
	if _, _, ok := hasDictionary(dir, lang); ok {
		return aff, dic, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	// Write to a temporary name and rename, so that another ge starting at
	// the same time never opens half a file.
	for _, f := range []struct {
		path string
		data []byte
	}{{aff, affData}, {dic, dicData}} {
		tmp := f.path + fmt.Sprintf(".tmp%d", os.Getpid())
		if err := os.WriteFile(tmp, f.data, 0o600); err != nil {
			return "", "", err
		}
		if err := os.Rename(tmp, f.path); err != nil {
			os.Remove(tmp)
			return "", "", err
		}
	}
	return aff, dic, nil
}

// ---------------------------------------------------------------------------
// The dictionaries of a language mode
// ---------------------------------------------------------------------------

// A language mode can have dictionaries of its own, next to the one of the
// language: the words of Go for the Go mode, and so on. They are the files
// named after the mode, in lower case, in the directories of the user's
// dictionaries ($GE_SPELL_DICT_DIR and <user config dir>/ge/spell/):
//
//	~/.config/ge/spell/go.dic     (required: the word list)
//	~/.config/ge/spell/go.aff     (optional: its affix file)
//
// Without a .aff the dictionary uses the affix file of the language. The
// words are accepted wherever the spell check looks: in comments and strings
// as well as in the identifiers.

// modeDict is a dictionary of a language mode.
type modeDict struct {
	name, aff, dic string
}

// userDictDirs are the directories of the user's own dictionaries.
func userDictDirs() []string {
	var dirs []string
	if d := os.Getenv("GE_SPELL_DICT_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	if d := userDictDir(); d != "" {
		dirs = append(dirs, d)
	}
	return dirs
}

// modeDictionaries finds the dictionaries of a language mode. mainAff is
// the affix file of the language, for a dictionary that has none of its own.
func modeDictionaries(mode, mainAff string) []modeDict {
	name := strings.ToLower(strings.TrimSpace(mode))
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil
	}

	var out []modeDict
	seen := map[string]bool{}
	for _, dir := range userDictDirs() {
		dic := filepath.Join(dir, name+".dic")
		if seen[dic] {
			continue // $GE_SPELL_DICT_DIR is the user's directory
		}
		seen[dic] = true
		if st, err := os.Stat(dic); err != nil || st.IsDir() {
			continue
		}
		aff := filepath.Join(dir, name+".aff")
		if st, err := os.Stat(aff); err != nil || st.IsDir() {
			aff = mainAff
		}
		out = append(out, modeDict{name: name, aff: aff, dic: dic})
	}
	return out
}
