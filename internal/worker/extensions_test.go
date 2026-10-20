package worker

import (
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/test/testutil"
	"strings"
	"testing"
)

func TestNewRejectsUncomposedExtensionsBeforeConsumingJobs(t *testing.T) {
	rdb := testutil.SetupTestRedis(t)
	if rdb == nil {
		t.Skip("TEST_REDIS_URL required")
	}
	for _, extensions := range []config.ExtensionsConfig{{ProtectedMode: true}, {Required: "cases"}, {RequiredInbound: "directory"}} {
		cfg := &config.Config{Extensions: extensions}
		if _, err := New(cfg, nil, rdb, testutil.NopLogger()); err == nil || !strings.Contains(err.Error(), "extension") {
			t.Fatalf("worker started or unrelated failure: %v", err)
		}
	}
}
