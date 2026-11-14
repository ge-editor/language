package lsp

// LanguageServerProvider is an optional interface of a language mode
// (gecore lang.Mode): a mode that implements it gets a language server.
//
// It is discovered with a type assertion, so gecore's Mode interface does
// not have to know about LSP, and adding a language needs no change to ge's
// core: implement these two methods on its mode.
//
//	if p, ok := mode.(lsp.LanguageServerProvider); ok { ... }
type LanguageServerProvider interface {
	// LSPLanguageID is the LSP languageId of the documents ("go",
	// "markdown", ...), which is not necessarily the mode's display name.
	LSPLanguageID() string

	// LSPServer says how to start the server. The Manager starts and
	// supervises it; the mode only describes it.
	LSPServer() ServerConfig
}
