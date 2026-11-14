package lsp

import "unicode/utf8"

// Position conversion between ge's coordinates and LSP's.
//
// ge's coordinates are (row, byte column) in UTF-8 text without newlines.
// An LSP Position is (line, character), where "character" counts in the
// position encoding negotiated at initialize:
//
//	utf-8  : bytes                  (identical to ge's column)
//	utf-16 : UTF-16 code units      (the protocol default)
//	utf-32 : Unicode code points
//
//	 text      A   あ   😀
//	 utf-8     1   3    4
//	 utf-16    1   1    2
//	 utf-32    1   1    1
//
// Neither is the display width of a character (East Asian Width, tabs):
// that is a third coordinate, used only for drawing, and never reaches
// this package.
//
// Conversion needs the text of the line, so it lives next to the
// Document that mirrors the buffer. For utf-16 and utf-32 it is O(length
// of the line); utf-8 is O(1).

// Position is an LSP position. Line and Character are zero-based.
type Position struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

// Range is an LSP range; End is exclusive.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// encodeColumn converts a byte column in line to an LSP character offset.
// A byteCol beyond the line is clamped to the end of the line. A byteCol in
// the middle of a character counts that whole character.
func encodeColumn(line []byte, byteCol int, enc string) uint32 {
	if byteCol < 0 {
		byteCol = 0
	}
	if byteCol > len(line) {
		byteCol = len(line)
	}
	if enc == PositionEncodingUTF8 {
		return uint32(byteCol)
	}

	var units uint32
	for i := 0; i < byteCol; {
		r, size := utf8.DecodeRune(line[i:])
		units += unitsOf(r, enc)
		i += size
	}
	return units
}

// decodeColumn converts an LSP character offset to a byte column in line.
// An offset beyond the end of the line yields len(line), as the protocol
// requires. An offset pointing into the middle of a character (between the
// two halves of a UTF-16 surrogate pair) is rounded down to the start of
// that character.
func decodeColumn(line []byte, character uint32, enc string) int {
	if enc == PositionEncodingUTF8 {
		if int64(character) > int64(len(line)) {
			return len(line)
		}
		return int(character)
	}

	var units uint32
	for i := 0; i < len(line); {
		if units >= character {
			return i
		}
		r, size := utf8.DecodeRune(line[i:])
		u := unitsOf(r, enc)
		if units+u > character {
			return i
		}
		units += u
		i += size
	}
	return len(line)
}

// unitsOf is the width of r in the encoding's units. Invalid UTF-8 decodes
// as U+FFFD one byte at a time, which is one unit in every encoding, so
// encodeColumn and decodeColumn stay consistent on broken text.
func unitsOf(r rune, enc string) uint32 {
	if enc == PositionEncodingUTF16 && r >= 0x10000 {
		return 2 // surrogate pair
	}
	return 1
}
