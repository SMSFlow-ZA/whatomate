// Package security provides opt-in durable MFA and approved emergency access.
// Host authentication must construct actors; no MFA flag is decoded from JSON.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238 / RFC 4226 interoperability, not password hashing.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

var (
	ErrDenied        = errors.New("security verification denied")
	ErrInvalid       = errors.New("invalid security configuration or input")
	ErrConflict      = errors.New("security credential version conflict")
	ErrCommitUnknown = errors.New("security commit outcome unknown; reconcile before retry")
)

// Secret prevents accidental serialization and formatting. Reveal is an explicit
// one-time trusted enrollment bridge operation; never log its returned value.
type Secret struct{ value string }

func newSecret(value string) Secret           { return Secret{value: value} }
func (s Secret) Reveal() string               { return s.value }
func (s Secret) String() string               { return "[REDACTED]" }
func (s Secret) GoString() string             { return "security.Secret([REDACTED])" }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

type secretCipher struct{ aead cipher.AEAD }

func newSecretCipher(key []byte) (secretCipher, error) {
	if len(key) != 32 {
		return secretCipher{}, ErrInvalid
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return secretCipher{}, ErrInvalid
	}
	aead, e := cipher.NewGCM(block)
	return secretCipher{aead: aead}, e
}
func (c secretCipher) seal(plain []byte, org, user, purpose string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return nil, e
	}
	aad, _ := json.Marshal([3]string{org, user, purpose})
	return c.aead.Seal(nonce, nonce, plain, aad), nil
}
func (c secretCipher) open(sealed []byte, org, user, purpose string) ([]byte, error) {
	size := c.aead.NonceSize()
	if len(sealed) < size {
		return nil, ErrDenied
	}
	aad, _ := json.Marshal([3]string{org, user, purpose})
	plain, e := c.aead.Open(nil, sealed[:size], sealed[size:], aad)
	if e != nil {
		return nil, ErrDenied
	}
	return plain, nil
}
func totp(secret []byte, counter uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(b[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 15
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}
func matchCounter(secret []byte, code string, now time.Time, drift int, last int64) (int64, bool) {
	if len(code) != 6 || drift < 0 || drift > 1 || now.Unix() < 0 {
		return 0, false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	step := now.Unix() / 30
	matched := int64(-1)
	for delta := -drift; delta <= drift; delta++ {
		candidate := step + int64(delta)
		if candidate < 0 || candidate <= last {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(totp(secret, uint64(candidate))), []byte(code)) == 1 && candidate > matched {
			matched = candidate
		}
	}
	return matched, matched >= 0
}
func randomToken() (string, error) {
	var b [20]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]), nil
}
func recoveryHash(org, user string, generation uint64, code string) []byte {
	payload, _ := json.Marshal([4]string{org, user, strconv.FormatUint(generation, 10), code})
	sum := sha256.Sum256(payload)
	return sum[:]
}
func activePurpose(generation uint64) string { return "active:" + strconv.FormatUint(generation, 10) }
