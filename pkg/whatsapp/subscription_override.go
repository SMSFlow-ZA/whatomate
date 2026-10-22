package whatsapp

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Validate checks the explicit alternate endpoint contract before any provider
// call. Error messages never contain the URL or verification token.
func (o WebhookSubscriptionOverride) Validate() error {
	u, err := url.Parse(o.CallbackURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		strings.TrimSpace(o.CallbackURL) != o.CallbackURL || utf8.RuneCountInString(o.CallbackURL) > 200 ||
		strings.TrimSpace(o.VerifyToken) == "" || strings.ContainsAny(o.VerifyToken, "\r\n") {
		return errors.New("invalid webhook subscription override")
	}
	return nil
}
