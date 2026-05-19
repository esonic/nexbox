package okone

import (
	"fmt"
	"strings"
	"testing"
)

func TestReadToken(t *testing.T) {
	token, err := ReadToken()
	if err != nil {
		t.Skipf("Skipping (expected in CI): %v", err)
	}
	if !strings.HasPrefix(token, "Bearer ") {
		t.Fatalf("Token should start with 'Bearer ', got: %s", token[:20])
	}
	fmt.Printf("Token (first 40 chars): %s...\n", token[:40])
}

func TestValidateTokenExpiry(t *testing.T) {
	// expired token (exp=1767614589, ~2026-01-05)
	expired := "Bearer eyJhbGciOiJFUzI1NiJ9.eyJleHAiOjE3Njc2MTQ1ODl9.sig"
	err := validateTokenExpiry(expired)
	if err != ErrTokenExpired {
		t.Fatalf("Expected ErrTokenExpired, got: %v", err)
	}

	// token with far future exp
	future := "Bearer eyJhbGciOiJFUzI1NiJ9.eyJleHAiOjk5OTk5OTk5OTl9.sig"
	err = validateTokenExpiry(future)
	if err != nil {
		t.Fatalf("Expected nil, got: %v", err)
	}
}
