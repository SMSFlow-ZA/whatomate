package middleware

import (
	"github.com/golang-jwt/jwt/v5"
	"testing"
	"time"
)

// Omitting freshness or accepting future proofs would let refreshed/stolen sessions bypass step-up.
func TestMFAProofRequiresFreshNonFutureVerification(t *testing.T) {
	now := time.Unix(1800000000, 0)
	cases := []struct {
		name string
		at   *jwt.NumericDate
		want bool
	}{
		{"absent", nil, false},
		{"fresh", jwt.NewNumericDate(now.Add(-time.Minute)), true},
		{"expired boundary", jwt.NewNumericDate(now.Add(-time.Hour)), false},
		{"future", jwt.NewNumericDate(now.Add(time.Second)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &JWTClaims{MFAAt: tc.at, MFAGeneration: 3}
			if got := c.MFAFresh(now, time.Hour); got != tc.want {
				t.Fatalf("fresh=%v want %v", got, tc.want)
			}
		})
	}
}
