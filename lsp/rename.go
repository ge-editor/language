package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// textDocument/rename.
//
// A rename answers with a WorkspaceEdit: edits for possibly many files. The
// Manager does not apply them (that is the editor's business, with its undo
// machinery); it returns them in ge coordinates together with what the
// editor needs to apply them safely:
//
//   - For every file that is an open buffer, the buffer version the edits
//     were computed for. The editor refuses to apply them if the buffer has
//     changed since: edits computed for other text would damage it.
//   - Every other open document of the same server is brought up to date
//     before the request is sent (a barrier), because the server renames
//     across all of them and must see their latest text, not text from
//     before some pending keystrokes.
//
// Files that are not open are read from disk to convert the positions.

var (
	// ErrStale: a buffer changed while the rename was being computed, so the
	// edits no longer match its text.
	ErrStale = errors.New("lsp: the text changed while the request was running")

	// ErrFileOperations: the server wants to create, rename or delete files
	// as part of the edit, which is not supported.
	ErrFileOperations = errors.New("lsp: the rename needs file operations (create, rename, delete), which are not supported")
)

// FileEdit is the edits to one file.
type FileEdit struct {
	Path string

	// Edits are in ge coordinates, valid for the file's text at the time of
	// the request, in no particular order. The server guarantees that they
	// do not overlap; check it anyway before relying on it.
	Edits []TextEdit

	// Open: the file is an open buffer of this Manager, and Version is the
	// buffer version the edits are valid for.
	Open    bool
	Version uint64
}

// WorkspaceEdit is the result of a rename: the files to edit, ordered by path.
type WorkspaceEdit struct {
	Files []FileEdit
}

// barrierTimeout bounds the wait for the other documents to be brought up to
// date. A document whose session cannot do it in time makes the rename fail
// rather than run against stale text.
const barrierTimeout = 3 * time.Second

type renameParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	NewName      string                 `json:"newName"`
}

// Rename asks the server to rename the symbol at pos to newName. done
// receives the edits on the UI goroutine, later; the result may have no
// files (nothing to rename).
func (m *Manager) Rename(buf Buffer, pos Pos, newName string, done func(*WorkspaceEdit, error)) {
	s := m.session(buf)
	if s == nil {
		done(nil, ErrNotOpen)
		return
	}

	versions, barrier := m.syncPoint(s)

	s.q.push(event{run: func(doc *Document, client *Client) {
		s.rename(doc, client, pos, newName, barrier, versions, done)
	}})
}

// syncPoint prepares a request that may involve several documents (rename,
// code actions): it records the versions of all open buffers as they are now,
// which is the text the server will have seen once the barrier has passed, and
// queues a barrier event on every other document of the same server. The
// session of the request waits for the barrier (waitTimeout) before sending,
// so the server has every document's latest edits when it answers.
//
// UI goroutine.
func (m *Manager) syncPoint(s *session) (map[*session]uint64, *sync.WaitGroup) {
	versions := make(map[*session]uint64, len(m.sessions))
	barrier := new(sync.WaitGroup)

	for _, other := range m.sessions {
		if other.closed {
			continue
		}
		versions[other] = other.buf.Version()

		if other == s || other.srv != s.srv {
			continue
		}
		select {
		case <-other.done:
			continue // its goroutine has ended; it cannot reach the barrier
		default:
		}

		// The session goroutine sends everything queued before this event
		// first, then runs it: when it fires, the server has that
		// document's edits.
		barrier.Add(1)
		other.q.push(event{run: func(*Document, *Client) { barrier.Done() }})
	}
	return versions, barrier
}

func (s *session) rename(doc *Document, client *Client, pos Pos, newName string,
	barrier *sync.WaitGroup, versions map[*session]uint64, done func(*WorkspaceEdit, error)) {

	fail := func(err error) { s.m.opts.Dispatch(func() { done(nil, err) }) }

	base, ok := s.prepare(doc, client, pos, client.Capabilities().HasRename(), fail)
	if !ok {
		return
	}
	if !waitTimeout(barrier, barrierTimeout) {
		fail(errors.New("lsp: the other open documents could not be brought up to date in time"))
		return
	}

	params := renameParams{TextDocument: base.TextDocument, Position: base.Position, NewName: newName}
	enc := doc.enc

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		var raw json.RawMessage
		if err := client.Conn().Call(ctx, "textDocument/rename", params, &raw); err != nil {
			fail(err)
			return
		}
		files, err := parseWorkspaceEdit(raw)
		if err != nil {
			fail(err)
			return
		}

		// Converting needs the text of the buffers, which the UI goroutine
		// may read (Snapshot) and which must be checked against the versions
		// recorded above there.
		s.m.opts.Dispatch(func() {
			we, err := s.m.finishRename(files, versions, enc)
			done(we, err)
		})
	}()
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// ---- wire format ----

type wireFileEdit struct {
	URI   string
	Edits []wireTextEdit
}

// parseWorkspaceEdit decodes a WorkspaceEdit into per-file edits, ordered by
// URI. Both forms are accepted ("changes" and "documentChanges", of which
// the latter is preferred when both are present, as the protocol asks); an
// entry that is a file operation is an error, never silently skipped.
func parseWorkspaceEdit(raw json.RawMessage) ([]wireFileEdit, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	var we struct {
		Changes         map[string][]wireTextEdit `json:"changes"`
		DocumentChanges []json.RawMessage         `json:"documentChanges"`
	}
	if err := json.Unmarshal(raw, &we); err != nil {
		return nil, fmt.Errorf("lsp: decoding workspace edit: %w", err)
	}

	byURI := make(map[string][]wireTextEdit)

	if len(we.DocumentChanges) > 0 {
		for _, item := range we.DocumentChanges {
			var dc struct {
				Kind         string `json:"kind"` // set for create / rename / delete
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Edits []wireTextEdit `json:"edits"`
			}
			if err := json.Unmarshal(item, &dc); err != nil {
				return nil, fmt.Errorf("lsp: decoding workspace edit: %w", err)
			}
			if dc.Kind != "" {
				return nil, ErrFileOperations
			}
			byURI[dc.TextDocument.URI] = append(byURI[dc.TextDocument.URI], dc.Edits...)
		}
	} else {
		for uri, edits := range we.Changes {
			byURI[uri] = append(byURI[uri], edits...)
		}
	}

	out := make([]wireFileEdit, 0, len(byURI))
	for uri, edits := range byURI {
		out = append(out, wireFileEdit{URI: uri, Edits: edits})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URI < out[j].URI })
	return out, nil
}

// finishRename converts the wire edits to ge coordinates and checks them
// against the buffers. UI goroutine.
func (m *Manager) finishRename(files []wireFileEdit, versions map[*session]uint64, enc string) (*WorkspaceEdit, error) {
	out := &WorkspaceEdit{}

	for _, f := range files {
		path, err := URIToPath(f.URI)
		if err != nil {
			return nil, err
		}
		fe := FileEdit{Path: path}

		var rows [][]byte

		m.uriMu.RLock()
		sess := m.byURI[f.URI]
		m.uriMu.RUnlock()

		if sess != nil && !sess.closed {
			// An open buffer: its text is what the server saw if its
			// version is the one recorded when the rename was asked for.
			var version uint64
			rows, version = sess.buf.Snapshot()
			if want, known := versions[sess]; !known || version != want {
				return nil, fmt.Errorf("%w: %s", ErrStale, path)
			}
			fe.Open, fe.Version = true, version
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("lsp: reading %s: %w", path, err)
			}
			rows = splitRows(data)
		}

		for _, w := range f.Edits {
			if w.Range == nil {
				continue
			}
			fe.Edits = append(fe.Edits, TextEdit{
				Start:   posIn(rows, w.Range.Start, enc),
				End:     posIn(rows, w.Range.End, enc),
				NewText: w.NewText,
			})
		}
		out.Files = append(out.Files, fe)
	}
	return out, nil
}

// splitRows splits file content into rows as a buffer holds them: at '\n',
// with a CR before it removed. There is always at least one row.
func splitRows(data []byte) [][]byte {
	rows := bytes.Split(data, []byte{'\n'})
	for i, r := range rows {
		rows[i] = bytes.TrimRight(r, "\r")
	}
	return rows
}

// posIn converts an LSP position to ge coordinates in rows. A position past
// the last line is the end of the text.
func posIn(rows [][]byte, p Position, enc string) Pos {
	if len(rows) == 0 {
		return Pos{}
	}
	if int64(p.Line) >= int64(len(rows)) {
		last := len(rows) - 1
		return Pos{Row: last, Col: len(rows[last])}
	}
	return Pos{Row: int(p.Line), Col: decodeColumn(rows[p.Line], p.Character, enc)}
}
