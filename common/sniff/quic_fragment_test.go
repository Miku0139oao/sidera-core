package sniff

import (
	"errors"
	"os"
	"testing"
)

func TestNormalizeQUICCryptoFragmentsHandlesRetransmissionAndOverlap(t *testing.T) {
	fragments := []qCryptoFragment{
		{offset: 0, length: 4, payload: []byte("abcd")},
		{offset: 0, length: 4, payload: []byte("abcd")},
		{offset: 2, length: 4, payload: []byte("cdef")},
		{offset: 6, length: 2, payload: []byte("gh")},
	}
	normalized, contiguous, err := normalizeQUICCryptoFragments(fragments)
	if err != nil {
		t.Fatal(err)
	}
	if string(contiguous) != "abcdefgh" {
		t.Fatalf("contiguous crypto data = %q", contiguous)
	}
	if len(normalized) != 1 || normalized[0].offset != 0 || string(normalized[0].payload) != "abcdefgh" {
		t.Fatalf("unexpected normalized fragments: %+v", normalized)
	}
}

func TestNormalizeQUICCryptoFragmentsPreservesHolesAndRejectsConflicts(t *testing.T) {
	normalized, contiguous, err := normalizeQUICCryptoFragments([]qCryptoFragment{
		{offset: 4, length: 2, payload: []byte("ef")},
		{offset: 0, length: 2, payload: []byte("ab")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(contiguous) != "ab" || len(normalized) != 2 {
		t.Fatalf("hole handling failed: contiguous=%q normalized=%+v", contiguous, normalized)
	}
	_, _, err = normalizeQUICCryptoFragments([]qCryptoFragment{
		{offset: 0, length: 4, payload: []byte("abcd")},
		{offset: 2, length: 2, payload: []byte("XY")},
	})
	if !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("conflicting overlap error = %v, want os.ErrInvalid", err)
	}
}
