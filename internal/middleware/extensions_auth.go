package middleware

import "time"

// MFAFresh never extends authentication freshness when tokens are refreshed.
func (c *JWTClaims) MFAFresh(now time.Time, freshness time.Duration) bool {
	return c != nil && c.MFAAt != nil && c.MFAGeneration > 0 && freshness > 0 && !c.MFAAt.Time.After(now) && now.Sub(c.MFAAt.Time) < freshness
}
