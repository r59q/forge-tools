package broker

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

// Sealer encrypts and authenticates secrets at rest. Open is a trusted primitive;
// applications must not expose it as a credential-reading API. Implementations
// must be safe for concurrent use and must not include plaintext in errors.
type Sealer interface {
	// Seal returns authenticated ciphertext without changing plaintext.
	Seal(plaintext []byte) ([]byte, error)
	// Open authenticates and decrypts sealed, or fails without returning plaintext.
	Open(sealed []byte) ([]byte, error)
}

// AESGCMSealer seals secrets using AES-256-GCM and a fresh random nonce per call.
// Persist the caller-provided key separately from the store. Its zero value is
// not usable; construct it with NewAESGCMSealer.
type AESGCMSealer struct {
	aead cipher.AEAD
}

var _ Sealer = (*AESGCMSealer)(nil)

// NewAESGCMSealer copies a 32-byte key into an AES-256-GCM cipher.
func NewAESGCMSealer(key []byte) (*AESGCMSealer, error) {
	if len(key) != 32 {
		return nil, ErrInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrSealing
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, ErrSealing
	}
	return &AESGCMSealer{aead: aead}, nil
}

// Seal returns ciphertext with an embedded random nonce and authentication tag.
func (s *AESGCMSealer) Seal(plaintext []byte) ([]byte, error) {
	if s == nil || s.aead == nil {
		return nil, ErrSealing
	}
	return s.aead.Seal(nil, nil, plaintext, nil), nil
}

// Open verifies ciphertext before returning plaintext. Modified, truncated and
// wrong-key ciphertext all fail with ErrSealing.
func (s *AESGCMSealer) Open(sealed []byte) ([]byte, error) {
	if s == nil || s.aead == nil {
		return nil, ErrSealing
	}
	plaintext, err := s.aead.Open(nil, nil, sealed, nil)
	if err != nil {
		return nil, ErrSealing
	}
	return plaintext, nil
}

// Format prevents fmt from inspecting cipher internals for any formatting verb.
func (*AESGCMSealer) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte("[REDACTED]")) }
