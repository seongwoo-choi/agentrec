package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	MaxProviderEventDepth  = 64
	MaxProviderEventTokens = 1_000_000
)

// ValidateProviderEvent applies the structural bounds shared by bundle writers
// and readers. remainingTokens is the unused token budget for the whole stream.
func ValidateProviderEvent(raw []byte, remainingTokens int) (int, error) {
	if remainingTokens <= 0 {
		return 0, fmt.Errorf("event stream holds more than %d JSON tokens", MaxProviderEventTokens)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	depth, tokens := 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if depth != 0 || tokens == 0 {
				return 0, errors.New("event is not a JSON object")
			}
			return tokens, nil
		}
		if err != nil {
			return 0, fmt.Errorf("event is not a JSON object: %w", err)
		}
		tokens++
		if tokens > remainingTokens {
			return 0, fmt.Errorf("event stream holds more than %d JSON tokens", MaxProviderEventTokens)
		}
		delim, isDelim := token.(json.Delim)
		if tokens == 1 && (!isDelim || delim != '{') {
			return 0, errors.New("event is not a JSON object")
		}
		if !isDelim {
			continue
		}
		switch delim {
		case '{', '[':
			depth++
			if depth > MaxProviderEventDepth {
				return 0, fmt.Errorf("event nesting exceeds %d", MaxProviderEventDepth)
			}
		case '}', ']':
			depth--
			if depth == 0 {
				if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
					return 0, errors.New("event holds more than one JSON value")
				}
				return tokens, nil
			}
		}
	}
}
