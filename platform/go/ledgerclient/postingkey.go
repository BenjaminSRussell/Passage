package ledgerclient

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	MaxKeyLen = 128

	postingKeyPrefix   = "post"
	postingKeySep      = ":"
	minPostingSegments = 5
)

var ErrInvalidPostingKey = errors.New("ledger: a posting key needs non-empty segments without ':' or whitespace and at most 128 characters")

func badSegment(s string) bool {
	return s == "" || strings.Contains(s, postingKeySep) || strings.IndexFunc(s, unicode.IsSpace) >= 0
}

func PostingKey(service, documentType, documentID, purpose string, revision ...string) (string, error) {
	segments := append([]string{postingKeyPrefix, service, documentType, documentID, purpose}, revision...)
	for _, s := range segments {
		if badSegment(s) {
			return "", fmt.Errorf("%w: segment %q", ErrInvalidPostingKey, s)
		}
	}
	key := strings.Join(segments, postingKeySep)
	if len(key) > MaxKeyLen {
		return "", fmt.Errorf("%w: %d characters", ErrInvalidPostingKey, len(key))
	}
	return key, nil
}

func MustPostingKey(service, documentType, documentID, purpose string, revision ...string) string {
	key, err := PostingKey(service, documentType, documentID, purpose, revision...)
	if err != nil {
		panic(err)
	}
	return key
}

func ValidPostingKey(key string) bool {
	segments := strings.Split(key, postingKeySep)
	if len(segments) < minPostingSegments || segments[0] != postingKeyPrefix || len(key) > MaxKeyLen {
		return false
	}
	for _, s := range segments {
		if s == "" {
			return false
		}
	}
	return true
}
