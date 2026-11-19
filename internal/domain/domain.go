// Package domain holds the few helpers every part of Falcon shares.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ErrConflict reports that what was changed is no longer what the caller saw.
var ErrConflict = errors.New("changed since it was read: reload and try again")

// ErrNotFound reports something that does not exist.
var ErrNotFound = errors.New("not found")

// Hash is the hex SHA-256 of b.
func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// JSON encodes v, ignoring errors (for values that always encode).
func JSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Prefix is at most `bytes` bytes of s, cut on a rune boundary.
func Prefix(s string, bytes int) string {
	if len(s) <= bytes {
		return s
	}
	for bytes > 0 && !utf8.RuneStart(s[bytes]) {
		bytes--
	}
	return s[:bytes]
}
