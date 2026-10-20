package security

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestTOTPStandardSHA1SixDigitVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		unix int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		if got := totp(secret, uint64(v.unix/30)); got != v.want {
			t.Fatalf("time %d got %s want %s", v.unix, got, v.want)
		}
	}
}
func TestTOTPDriftAndReplayCounter(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1234567890, 0)
	step := now.Unix() / 30
	for _, delta := range []int64{-1, 0, 1} {
		code := totp(secret, uint64(step+delta))
		if counter, ok := matchCounter(secret, code, now, 1, step-2); !ok || counter != step+delta {
			t.Fatalf("drift %d rejected", delta)
		}
	}
	if _, ok := matchCounter(secret, totp(secret, uint64(step+2)), now, 1, -1); ok {
		t.Fatal("accepted excessive drift")
	}
	if _, ok := matchCounter(secret, totp(secret, uint64(step)), now, 1, step); ok {
		t.Fatal("replayed counter")
	}
	for _, code := range []string{"12345", "1234567", "abcdef", " 005924"} {
		if _, ok := matchCounter(secret, code, now, 1, -1); ok {
			t.Fatal("malformed code accepted")
		}
	}
}
func TestSecretsAreRedactedAndCiphertextBoundToIdentity(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cipher, e := newSecretCipher(key)
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := cipher.seal([]byte("secret-value"), "org", "user", "purpose")
	if e != nil {
		t.Fatal(e)
	}
	opened, e := cipher.open(sealed, "org", "user", "purpose")
	if e != nil || string(opened) != "secret-value" {
		t.Fatal("cannot decrypt own secret")
	}
	if _, e = cipher.open(sealed, "org", "other", "purpose"); e == nil {
		t.Fatal("ciphertext identity substitution accepted")
	}
	sealed[len(sealed)-1] ^= 1
	if _, e = cipher.open(sealed, "org", "user", "purpose"); e == nil {
		t.Fatal("ciphertext tampering accepted")
	}
	secret := newSecret("sensitive-material")
	for _, got := range []string{fmt.Sprintf("%v", secret), fmt.Sprintf("%+v", secret), fmt.Sprintf("%#v", secret)} {
		if strings.Contains(got, "sensitive-material") {
			t.Fatal("formatted secret leaked")
		}
	}
	raw, e := json.Marshal(secret)
	if e != nil || strings.Contains(string(raw), "sensitive-material") {
		t.Fatal("JSON secret leaked")
	}
	if secret.Reveal() != "sensitive-material" {
		t.Fatal("explicit trusted reveal failed")
	}
	if _, e = newSecretCipher([]byte("short")); e == nil {
		t.Fatal("weak master key accepted")
	}
}

func TestBreakGlassReasonAndConfigurationRejectBlankOrBroadInputs(t *testing.T) {
	for _, reason := range []string{"", "   ", "\nincident", "ok"} {
		if validReason(reason) {
			t.Fatalf("invalid reason accepted %q", reason)
		}
	}
	if !validReason("incident:synthetic-1") {
		t.Fatal("valid reference reason denied")
	}
	db := new(sql.DB)
	key := make([]byte, 32)
	for _, cfg := range []Config{{Drift: 2, EnrollmentTTL: time.Minute, MaxBreakGlass: time.Minute}, {Drift: -1, EnrollmentTTL: time.Minute, MaxBreakGlass: time.Minute}, {Drift: 1, EnrollmentTTL: 0, MaxBreakGlass: time.Minute}, {Drift: 1, EnrollmentTTL: time.Minute, MaxBreakGlass: 0}} {
		if _, e := New(db, key, cfg); e == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
}
