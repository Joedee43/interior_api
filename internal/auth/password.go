package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params configure Argon2id. They are stored inside every hash
// (PHC string format), so raising them later only affects new hashes;
// existing passwords still verify with the parameters they were made with.
type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLen     uint32
	KeyLen      uint32
}

// DefaultArgon2Params follow RFC 9106 §4's second recommended option
// (64 MiB, t=1, p=4). Roughly 50–100 ms per hash on a typical machine.
var DefaultArgon2Params = Argon2Params{
	MemoryKiB:   64 * 1024,
	Iterations:  1,
	Parallelism: 4,
	SaltLen:     16,
	KeyLen:      32,
}

// PasswordHasher hashes and verifies passwords. It is a value with its
// own parameters (not package functions) so tests can use cheap ones.
type PasswordHasher struct {
	params Argon2Params
}

func NewPasswordHasher(p Argon2Params) PasswordHasher { return PasswordHasher{params: p} }

var errMalformedHash = errors.New("malformed password hash")

// Hash returns a self-describing string like
// $argon2id$v=19$m=65536,t=1,p=4$<salt>$<key> (base64, no padding).
func (h PasswordHasher) Hash(password string) (string, error) {
	p := h.params
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches encoded. A non-nil error means
// the stored hash itself is unusable, not that the password was wrong.
func (h PasswordHasher) Verify(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Iterations, &p.Parallelism); err != nil {
		return false, errMalformedHash
	}
	// Refuse absurd parameters so a tampered row cannot make us allocate
	// gigabytes of memory.
	if p.MemoryKiB == 0 || p.MemoryKiB > 1024*1024 || p.Iterations == 0 || p.Iterations > 16 || p.Parallelism == 0 {
		return false, errMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errMalformedHash
	}

	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want)))
	// Constant-time comparison: the time taken does not depend on how many
	// leading bytes match, so it leaks nothing about the stored key.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
