package handlers

import (
	"context"
	"github.com/google/uuid"
	"testing"
)

func TestGenericProtectionMissingDatabaseDenies(t *testing.T) {
	app := &App{}
	if app.GenericOrganizationAllowed(context.Background(), uuid.New()) {
		t.Fatal("missing policy storage allowed")
	}
}
