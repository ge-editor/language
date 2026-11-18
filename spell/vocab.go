package spell

import "strings"

// programmingTerms are words of code that every programmer knows and that no
// dictionary has: abbreviations, the names of the standard library, the words
// of C and Go. The check of identifiers (codeTokens) does not flag them. It
// is lower case; the word is lower-cased before the lookup.
//
// Words of fewer than codeMinLetters letters are not checked at all, so only
// the longer ones are listed. A word that is not here is added with ESC x
// spell-add (the personal dictionary), for good.
var programmingTerms = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, w := range strings.Fields(vocabulary) {
		m[w] = struct{}{}
	}
	return m
}()

const vocabulary = `
args argv argc attr attrs authn authz atoi itoa utoa ansi ascii async await
backend bufio bufs bitmask bitwise bash
calloc cfg chan chdir cmds conn conns ctxt cwd cgo
dedup dedupe dedupes deinit deque dequeue dest dir dirs dockerfile docker
elem elems enqueue enum envs errno errorf eof endian
fatalf fallthrough fprintf fprintln frontend func funcs
gofmt golang golint gopls goroutine goroutines goto govet github gitlab gitignore
hdr html http https hunspell
impl init inode iota iter ioutil
json
kubectl
lang langs lexer lexers libc linux lsp
macos makefile malloc marshal marshaler middleware memcpy memset mkdir mutex mutexes msgs
nolint nullptr
opts oauth
panicf param params parseint parsefloat perf pkgs plugin plugins posix printf println proc procs prefs ptrs
readdir readfile readme realloc regex regexp regexps repo repos resp rwmutex runtime
sigint sigterm sizeof sprintf sprintln sscanf stat statfs stdin stdout stderr stdio stdlib stddef stdint
strcmp strcpy strlen strconv struct structs sudo syscall syscalls sysctl
tcell tmpl toml tokenizer tokenizers treesitter tuple tuples typedef typeof
uint uint8 uint16 uint32 uint64 uintptr unistd unix unmarshal unmarshaler utf utils
vals varint vars
waitgroup wasm webhook writefile
xdg
yaml
zsh
`
