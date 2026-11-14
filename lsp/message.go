// Package lsp implements the client side of the Language Server Protocol.
//
// Layering (see also the notes at the top of each file):
//
//	transport.go : Content-Length framing over an io.Reader / io.Writer
//	message.go   : JSON-RPC 2.0 wire types and error codes
//	protocol.go  : Conn, request/response matching, cancellation
//
// Nothing in this package knows about EditBuffer, Leaf or Workspace, and it
// uses only the Go standard library. LSP semantics (initialize, document
// synchronization, ...) are built on top of Conn in later files.
package lsp

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 and LSP error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// LSP specific.
	CodeServerNotInitialized = -32002
	CodeRequestCancelled     = -32800
	CodeContentModified      = -32801
)

// ResponseError is the error object of a JSON-RPC response. It implements
// error, so a failed Conn.Call returns it directly and callers can use
// errors.As to inspect Code.
type ResponseError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("lsp: server error %d: %s", e.Code, e.Message)
}

// wireMessage is the union of request, notification and response.
//
//	request      : Method != "" && ID != nil
//	notification : Method != "" && ID == nil
//	response     : Method == "" && ID != nil (Result or Error)
//
// ID is kept raw because it may be a number or a string, and a response
// must echo the id of the request byte for byte.
//
// Result is a RawMessage so that an explicit `"result": null` (a valid
// success response) survives: omitempty only drops a zero-length slice,
// and a "null" literal has length 4.
type wireMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *ResponseError   `json:"error,omitempty"`
}

// cancelParams is the params of the $/cancelRequest notification.
type cancelParams struct {
	ID json.RawMessage `json:"id"`
}
