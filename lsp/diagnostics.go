package lsp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// textDocument/publishDiagnostics.
//
// The server pushes the complete list of diagnostics of a document each
// time its analysis finishes (an empty list clears them). Positions are in
// the document text the server had at that moment; with versionSupport,
// announced in initialize, the message names that version. The session
// converts positions to ge coordinates using its mirror, and only for the
// version the mirror currently describes: a result for older text is
// dropped, because the server will publish again after it has seen the
// newer text, and ranges converted against the wrong text would be wrong.

// DiagnosticSeverity is the LSP DiagnosticSeverity.
type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

// Diagnostic is one diagnostic with its range in ge coordinates: (row, byte
// column), End exclusive. Start == End is possible (a point).
type Diagnostic struct {
	Start, End Pos
	Severity   DiagnosticSeverity
	Code       string
	Source     string
	Message    string
}

// DiagnosticsUpdate is the full replacement set of diagnostics of a buffer.
type DiagnosticsUpdate struct {
	// Version is the buffer version (EditBuffer.Version) whose text the
	// positions refer to. A consumer that finds the buffer at another
	// version must ignore the update.
	Version uint64
	Items   []Diagnostic
}

type publishDiagnosticsParams struct {
	URI         string           `json:"uri"`
	Version     *int32           `json:"version"`
	Diagnostics []wireDiagnostic `json:"diagnostics"`
}

type wireDiagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity,omitempty"`
	Code     json.RawMessage `json:"code,omitempty"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}

func parsePublishDiagnostics(params json.RawMessage) (*publishDiagnosticsParams, error) {
	var p publishDiagnosticsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("lsp: publishDiagnostics: %w", err)
	}
	return &p, nil
}

// convertDiagnostics maps wire diagnostics to ge coordinates using the
// mirror's current text.
func (d *Document) convertDiagnostics(list []wireDiagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(list))
	for _, w := range list {
		sev := DiagnosticSeverity(w.Severity)
		if sev < SeverityError || sev > SeverityHint {
			sev = SeverityError // the protocol leaves it to the client; an error is the safe reading
		}
		out = append(out, Diagnostic{
			Start:    d.FromLSP(w.Range.Start),
			End:      d.FromLSP(w.Range.End),
			Severity: sev,
			Code:     codeString(w.Code),
			Source:   w.Source,
			Message:  w.Message,
		})
	}
	return out
}

// codeString renders a diagnostic code, which is a number or a string.
func codeString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return s
}
