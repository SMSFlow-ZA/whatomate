package channelapp

import (
	"context"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestProtectedConnectionQueryExcludesBeforeLimit(t *testing.T) {
	db, e := gorm.Open(postgres.New(postgres.Config{DSN: "host=synthetic.invalid user=synthetic dbname=synthetic sslmode=disable"}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if e != nil {
		t.Fatal(e)
	}
	id := uuid.New()
	s := Service{ProtectedConnectionIDs: []uuid.UUID{id}}
	var rows []models.ChannelConnection
	q := s.GenericConnections(db).Order("next_poll_at").Limit(20).Find(&rows)
	if q.Error != nil {
		t.Fatal(q.Error)
	}
	sql := q.Statement.SQL.String()
	if !strings.Contains(sql, "id NOT IN") || !strings.Contains(sql, "LIMIT") || len(q.Statement.Vars) < 1 || q.Statement.Vars[0] != id {
		t.Fatal("missing parameterized pre-limit exclusion", sql, q.Statement.Vars)
	}
	var attempts []models.ChannelAttempt
	q = s.GenericConnectionReferences(db, "connection_id").Limit(20).Find(&attempts)
	if !strings.Contains(q.Statement.SQL.String(), "connection_id NOT IN") {
		t.Fatal("attempt exclusion missing", q.Statement.SQL.String())
	}
}
func TestProtectedConnectionRejectsGenericEntryPointsBeforeStorage(t *testing.T) {
	id := uuid.New()
	s := Service{ProtectedConnectionIDs: []uuid.UUID{id}}
	if _, e := s.EnqueueSMSWithID(context.Background(), uuid.New(), uuid.New(), id, uuid.New(), "synthetic", nil, true, "chat"); e == nil {
		t.Fatal("protected enqueue accepted")
	}
	if e := s.AttachFallback(context.Background(), nil, id, "synthetic", true, 0); e == nil {
		t.Fatal("protected fallback accepted")
	}
	if e := s.PollConnection(context.Background(), id); e == nil {
		t.Fatal("protected poll accepted")
	}
	if _, e := s.connection(nil, uuid.New(), id); e == nil {
		t.Fatal("protected connection accepted")
	}
	if !s.IsConnectionProtected(id) || s.IsConnectionProtected(uuid.New()) {
		t.Fatal("incorrect immutable binding exclusion")
	}
}
func TestProtectedConnectionNeverResolvesInGenericRuntime(t *testing.T) {
	key := strings.Repeat("k", 32)
	encrypted, e := crypto.Encrypt(`{"client_id":"synthetic","client_secret":"synthetic"}`, key)
	if e != nil {
		t.Fatal(e)
	}
	registry := channels.NewRegistry()
	if e = registry.Register(&testProtectedProvider{}); e != nil {
		t.Fatal(e)
	}
	id := uuid.New()
	s := Service{Providers: registry, EncryptionKey: key, ConnectionProtected: func(candidate uuid.UUID) bool { return candidate == id }}
	c := models.ChannelConnection{BaseModel: models.BaseModel{ID: id}, Provider: "test-private", Credentials: encrypted}
	if _, _, e = s.runtime(&c); e == nil {
		t.Fatal("generic runtime resolved protected connection")
	}
}
