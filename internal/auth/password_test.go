package auth

import (
	"strings"
	"testing"
)

// cheapParams keep tests fast; production uses DefaultArgon2Params.
var cheapParams = Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func TestPasswordHasher_RoundTrip(t *testing.T) {
	h := NewPasswordHasher(cheapParams)

	hash, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("unexpected hash format: %s", hash)
	}

	ok, err := h.Verify("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Errorf("Verify(correct) = %v, %v; want true, nil", ok, err)
	}
	ok, err = h.Verify("wrong horse battery staple", hash)
	if err != nil || ok {
		t.Errorf("Verify(wrong) = %v, %v; want false, nil", ok, err)
	}
}

func TestPasswordHasher_SaltIsRandom(t *testing.T) {
	h := NewPasswordHasher(cheapParams)
	a, _ := h.Hash("same password here")
	b, _ := h.Hash("same password here")
	if a == b {
		t.Error("two hashes of the same password are identical; salt is not random")
	}
}

func TestPasswordHasher_VerifiesWithStoredParams(t *testing.T) {
	// A hash made with other parameters must still verify: params come
	// from the hash string, not from the hasher doing the verifying.
	old, _ := NewPasswordHasher(Argon2Params{MemoryKiB: 128, Iterations: 2, Parallelism: 2, SaltLen: 8, KeyLen: 16}).Hash("an older password")
	ok, err := NewPasswordHasher(cheapParams).Verify("an older password", old)
	if err != nil || !ok {
		t.Errorf("Verify with different hasher params = %v, %v; want true, nil", ok, err)
	}
}

func TestPasswordHasher_MalformedHash(t *testing.T) {
	h := NewPasswordHasher(cheapParams)
	for _, bad := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdA$a2V5", // wrong variant
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdA$a2V5",      // wrong version
		"$argon2id$v=19$m=9999999,t=1,p=1$c2FsdA$a2V5", // absurd memory
		"$argon2id$v=19$m=64,t=1,p=1$!!!$a2V5",         // bad base64
	} {
		if _, err := h.Verify("whatever", bad); err == nil {
			t.Errorf("Verify(%q) returned no error", bad)
		}
	}
}
