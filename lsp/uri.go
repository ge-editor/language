package lsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// FileURI returns the file:// URI of path. A relative path is made
// absolute first. Characters outside the URI path alphabet (spaces,
// non-ASCII, ...) are percent-encoded as UTF-8.
func FileURI(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows: C:/dir -> /C:/dir
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// URIToPath converts a file:// URI back to a filesystem path.
func URIToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("lsp: not a file URI: %q", uri)
	}

	p := u.Path // already percent-decoded
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), nil
}
