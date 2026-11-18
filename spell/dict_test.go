package spell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindDictionaryInTheConfiguredDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"xx_YY.aff", "xx_YY.dic"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GE_SPELL_DICT_DIR", dir)

	aff, dic, err := findDictionary("xx_YY")
	if err != nil {
		t.Fatal(err)
	}
	if aff != filepath.Join(dir, "xx_YY.aff") || dic != filepath.Join(dir, "xx_YY.dic") {
		t.Fatalf("%s %s", aff, dic)
	}
}

func TestFindDictionaryTellsWhereToPutOne(t *testing.T) {
	t.Setenv("GE_SPELL_DICT_DIR", t.TempDir())
	_, _, err := findDictionary("zz_ZZ")
	if err == nil || !strings.Contains(err.Error(), "zz_ZZ.aff") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := findDictionary("../etc/passwd"); err == nil {
		t.Fatal("a path was accepted as a language")
	}
}

func TestModeDictionaries(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("0\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.dic")
	write("go.aff")
	write("org.dic") // no .aff: the one of the language is used
	t.Setenv("GE_SPELL_DICT_DIR", dir)

	got := modeDictionaries("Go", "/main.aff")
	if len(got) != 1 || got[0].name != "go" ||
		got[0].dic != filepath.Join(dir, "go.dic") || got[0].aff != filepath.Join(dir, "go.aff") {
		t.Fatalf("Go: %+v", got)
	}

	got = modeDictionaries("Org", "/main.aff")
	if len(got) != 1 || got[0].aff != "/main.aff" || got[0].dic != filepath.Join(dir, "org.dic") {
		t.Fatalf("Org: %+v", got)
	}

	// None: no file, an empty name, a name that is a path.
	for _, mode := range []string{"Markdown", "", "  ", "..", "../go", "a/b"} {
		if got := modeDictionaries(mode, "/main.aff"); len(got) != 0 {
			t.Errorf("%q: %+v", mode, got)
		}
	}
}
