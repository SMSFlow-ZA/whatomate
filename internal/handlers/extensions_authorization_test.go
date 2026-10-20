package handlers

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/test/testutil"
)

func TestGeneralAuthorizationDelegatesOnlyKnownExtensionBoundaries(t *testing.T) {
	a := &App{ExtensionAPI: map[string]http.Handler{"cases": http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}}
	for _, v := range []struct {
		path, method string
		allowed      bool
	}{
		{"/api/extensions/security/mfa", "GET", true}, {"/api/extensions/security/mfa/enroll", "POST", true},
		{"/api/extensions/catalog", "GET", true}, {"/api/extensions/cases", "GET", true}, {"/api/extensions/cases/a/state", "POST", true},
		{"/api/extensions/unknown/data", "GET", false}, {"/api/extensions/security/backdoor", "POST", false}, {"/api/extensions/catalog", "POST", false},
	} {
		r := testutil.NewJSONRequest(t, map[string]any{})
		r.RequestCtx.Request.SetRequestURI(v.path)
		r.RequestCtx.Request.Header.SetMethod(v.method)
		testutil.SetAuthContext(r, uuid.New(), uuid.New())
		if got := a.AuthorizeAPI()(r) != nil; got != v.allowed {
			t.Fatalf("%s %s delegation %v", v.method, v.path, got)
		}
	}
}
