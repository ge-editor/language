package lsp

// Content-Length framing used by LSP over stdio:
//
//	Content-Length: <n>\r\n
//	[Content-Type: ...\r\n]
//	\r\n
//	<n bytes of UTF-8 JSON>
//
// Header names are case-insensitive; headers other than Content-Length are
// accepted and ignored.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxFrameSize bounds the memory a single message may claim. A server
// returning a huge workspace symbol list is the realistic worst case.
const maxFrameSize = 256 << 20

// readFrame reads one framed message body.
//
// It returns io.EOF only when the stream ended cleanly between messages;
// an end of stream in the middle of a message is io.ErrUnexpectedEOF.
func readFrame(r *bufio.Reader) ([]byte, error) {
	contentLength := -1
	sawHeader := false

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" && !sawHeader {
				return nil, io.EOF
			}
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		sawHeader = true

		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("lsp: malformed header line %q", line)
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("lsp: invalid Content-Length %q", value)
			}
			contentLength = n
		}
	}

	if contentLength < 0 {
		return nil, fmt.Errorf("lsp: missing Content-Length header")
	}
	if contentLength > maxFrameSize {
		return nil, fmt.Errorf("lsp: message too large (%d bytes)", contentLength)
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return body, nil
}

// writeFrame writes body with its header in a single Write call, so that
// callers holding a mutex around it never interleave partial messages.
func writeFrame(w io.Writer, body []byte) error {
	var buf bytes.Buffer
	buf.Grow(len(body) + 32)
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)

	_, err := w.Write(buf.Bytes())
	return err
}
