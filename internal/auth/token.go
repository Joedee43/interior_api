package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// newSessionToken returns 32 random bytes (256 bits) as URL-safe base64.
// The raw token only ever exists in the client's cookie; the database
// stores hashToken(raw).
func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is a plain SHA-256, not Argon2: the token already has 256 bits
// of randomness, so it cannot be brute-forced, and it is hashed on every
// authenticated request, so it must be fast.
func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
