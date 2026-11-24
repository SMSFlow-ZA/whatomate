package handlers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Review confirmations are opaque and process-scoped. A restart requires a new
// preview; durable submission identifiers and reconciliation are independent.
var inboxPreviewSigningKey = func() [32]byte {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		panic("cannot initialise inbox preview signing")
	}
	return key
}()

func inboxPreviewConfirmation(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, inboxPreviewSigningKey[:])
	// hash.Hash.Write cannot return an error.
	_, _ = mac.Write([]byte("inbox-preview:v1\x00"))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func inboxPreviewMatches(expected, supplied string) bool {
	return len(expected) == 64 && len(supplied) == 64 && hmac.Equal([]byte(expected), []byte(supplied))
}
