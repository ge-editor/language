package lsp

import (
	"bytes"
	"encoding/json"
)

// Position encodings (LSP 3.17, general.positionEncodings).
//
// ge keeps (row, byte column) everywhere, which is exactly "utf-8". The
// client offers utf-8 first; a server that supports it lets document.go
// pass byte columns through unchanged. Servers that only speak the
// mandatory utf-16 need a conversion using the line text.
const (
	PositionEncodingUTF8  = "utf-8"
	PositionEncodingUTF16 = "utf-16"
	PositionEncodingUTF32 = "utf-32"
)

// defaultClientCapabilities is what ge announces in initialize.
//
// It deliberately announces only what ge implements so far, and no
// dynamicRegistration: servers then use static capabilities and do not
// bombard the client with client/registerCapability.
func defaultClientCapabilities() map[string]any {
	return map[string]any{
		"general": map[string]any{
			"positionEncodings": []string{PositionEncodingUTF8, PositionEncodingUTF16},
		},
		"workspace": map[string]any{
			// Edits to text documents only: no create / rename / delete.
			"workspaceEdit": map[string]any{
				"documentChanges":    true,
				"resourceOperations": []string{},
			},
		},
		"textDocument": map[string]any{
			"synchronization": map[string]any{
				"dynamicRegistration": false,
				"willSave":            false,
				"willSaveWaitUntil":   false,
				"didSave":             true,
			},
			"publishDiagnostics": map[string]any{
				"versionSupport": true,
			},
			"documentSymbol": map[string]any{
				"dynamicRegistration":               false,
				"hierarchicalDocumentSymbolSupport": true,
			},
			"formatting": map[string]any{"dynamicRegistration": false},
			"codeAction": map[string]any{
				"dynamicRegistration": false,
				// Without literal support servers answer with bare commands
				// only. Edits are expected inline: no resolve support.
				"codeActionLiteralSupport": map[string]any{
					"codeActionKind": map[string]any{"valueSet": []string{
						"quickfix", "refactor", "refactor.extract", "refactor.inline",
						"refactor.rewrite", "source", "source.organizeImports", "source.fixAll",
					}},
				},
				"isPreferredSupport": true,
				"disabledSupport":    true,
			},
			"rename": map[string]any{
				"dynamicRegistration": false,
				"prepareSupport":      false,
			},
			"completion": map[string]any{
				// Plain text edits only: no snippets, no insert/replace pairs.
				"completionItem": map[string]any{
					"snippetSupport":      false,
					"insertReplaceSupport": false,
					"documentationFormat": []string{"plaintext"},
				},
			},
		},
	}
}

// SyncKind is TextDocumentSyncKind.
type SyncKind int

const (
	SyncNone        SyncKind = 0
	SyncFull        SyncKind = 1
	SyncIncremental SyncKind = 2
)

// SyncOptions is the normalized form of ServerCapabilities.textDocumentSync,
// which a server may send as a bare number or as an object.
type SyncOptions struct {
	OpenClose       bool     // wants didOpen / didClose
	Change          SyncKind // how didChange carries edits
	Save            bool     // wants didSave
	SaveIncludeText bool     // didSave must carry the full text
}

// ServerCapabilities is the part of the server's capabilities ge looks at.
// Provider fields are kept raw: each is `true`, an options object, or
// absent / `false`; use the Has* methods.
type ServerCapabilities struct {
	PositionEncoding string          `json:"positionEncoding,omitempty"`
	TextDocumentSync json.RawMessage `json:"textDocumentSync,omitempty"`

	HoverProvider              json.RawMessage `json:"hoverProvider,omitempty"`
	DefinitionProvider         json.RawMessage `json:"definitionProvider,omitempty"`
	ReferencesProvider         json.RawMessage `json:"referencesProvider,omitempty"`
	RenameProvider             json.RawMessage `json:"renameProvider,omitempty"`
	CompletionProvider         json.RawMessage `json:"completionProvider,omitempty"`
	DocumentFormattingProvider json.RawMessage `json:"documentFormattingProvider,omitempty"`
	DocumentSymbolProvider     json.RawMessage `json:"documentSymbolProvider,omitempty"`
	CodeActionProvider         json.RawMessage `json:"codeActionProvider,omitempty"`
	WorkspaceSymbolProvider    json.RawMessage `json:"workspaceSymbolProvider,omitempty"`
}

// Encoding returns the position encoding the server chose. A server that
// does not know the field uses utf-16, which is the protocol default.
func (s ServerCapabilities) Encoding() string {
	if s.PositionEncoding == "" {
		return PositionEncodingUTF16
	}
	return s.PositionEncoding
}

// Sync normalizes textDocumentSync.
func (s ServerCapabilities) Sync() SyncOptions {
	raw := bytes.TrimSpace(s.TextDocumentSync)
	if len(raw) == 0 || string(raw) == "null" {
		return SyncOptions{}
	}

	// Number form: the server only states the change kind. Such servers
	// still expect didOpen / didClose, so OpenClose is true.
	var kind int
	if json.Unmarshal(raw, &kind) == nil {
		return SyncOptions{OpenClose: true, Change: SyncKind(kind)}
	}

	var obj struct {
		OpenClose bool            `json:"openClose"`
		Change    int             `json:"change"`
		Save      json.RawMessage `json:"save"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return SyncOptions{}
	}

	opts := SyncOptions{OpenClose: obj.OpenClose, Change: SyncKind(obj.Change)}

	// save is `true`, `false`, or {includeText?: boolean}.
	save := bytes.TrimSpace(obj.Save)
	switch {
	case len(save) == 0, string(save) == "null", string(save) == "false":
	case string(save) == "true":
		opts.Save = true
	default:
		var so struct {
			IncludeText bool `json:"includeText"`
		}
		if json.Unmarshal(save, &so) == nil {
			opts.Save = true
			opts.SaveIncludeText = so.IncludeText
		}
	}
	return opts
}

func (s ServerCapabilities) HasHover() bool { return providerEnabled(s.HoverProvider) }
func (s ServerCapabilities) HasDefinition() bool { return providerEnabled(s.DefinitionProvider) }
func (s ServerCapabilities) HasReferences() bool { return providerEnabled(s.ReferencesProvider) }
func (s ServerCapabilities) HasRename() bool { return providerEnabled(s.RenameProvider) }
func (s ServerCapabilities) HasCompletion() bool { return providerEnabled(s.CompletionProvider) }
func (s ServerCapabilities) HasFormatting() bool { return providerEnabled(s.DocumentFormattingProvider) }

func (s ServerCapabilities) HasCodeAction() bool { return providerEnabled(s.CodeActionProvider) }

func (s ServerCapabilities) HasDocumentSymbol() bool {
	return providerEnabled(s.DocumentSymbolProvider)
}
func (s ServerCapabilities) HasWorkspaceSymbol() bool {
	return providerEnabled(s.WorkspaceSymbolProvider)
}

func providerEnabled(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	switch string(raw) {
	case "null", "false":
		return false
	}
	return true
}
