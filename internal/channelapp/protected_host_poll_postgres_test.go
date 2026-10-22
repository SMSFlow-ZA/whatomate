package channelapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type hostPollProvider struct {
	testProtectedProvider
	calls        int
	page         channels.EventPage
	beforeReturn func()
	failure      error
}

func (p *hostPollProvider) Poll(context.Context, channels.Connection, string) (channels.EventPage, error) {
	p.calls++
	if p.beforeReturn != nil {
		p.beforeReturn()
	}
	return p.page, p.failure
}

func protectedHostPollFixture(t *testing.T) (*Service, *hostPollProvider, models.ChannelConnection, models.Contact) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("protected_poll_%d", time.Now().UnixNano())
	if _, err = base.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	if err = db.AutoMigrate(&models.Contact{}, &models.Message{}, &models.ChannelConnection{}, &models.ChannelAttempt{}, &models.ChannelReceipt{}, &models.ChannelBotJob{}); err != nil {
		t.Fatal(err)
	}
	if err = models.InstallProtectedHostGuard(db); err != nil {
		t.Fatal(err)
	}
	p := &hostPollProvider{}
	registry := channels.NewRegistry()
	if err = registry.Register(p); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("k", 32)
	encrypted, err := crypto.Encrypt(`{"client_id":"synthetic"}`, key)
	if err != nil {
		t.Fatal(err)
	}
	org := uuid.New()
	now := time.Now().UTC()
	connection := models.ChannelConnection{OrganizationID: org, Name: "ordinary-before-ER", Provider: "test-private", Enabled: true, Credentials: encrypted, Capabilities: models.JSONB{}, ValidatedAt: &now, NextPollAt: now.Add(-time.Hour)}
	if err = db.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	contact := models.Contact{OrganizationID: org, PhoneNumber: "+27820000001", LastMessagePreview: "old preview"}
	if err = db.Create(&contact).Error; err != nil {
		t.Fatal(err)
	}
	message := models.Message{OrganizationID: org, ContactID: contact.ID, Channel: "sms", ChannelConnectionID: &connection.ID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "old outbound", Status: models.MessageStatusSent}
	if err = db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	attempt := models.ChannelAttempt{OrganizationID: org, MessageID: message.ID, LogicalMessageID: message.ID, ConnectionID: &connection.ID, Channel: "sms", Status: "submitted", Destination: contact.PhoneNumber}
	if err = db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	p.page = channels.EventPage{NextCursor: "new-cursor", Events: []channels.Event{{ID: "new-reply", Kind: "inbound", ClientMessageID: attempt.ID.String(), Phone: contact.PhoneNumber, Text: "PRIVATE-INBOUND-MARKER", OccurredAt: now}}}
	return &Service{DB: db, Providers: registry, EncryptionKey: key}, p, connection, contact
}

func assertProtectedPollPreserved(t *testing.T, s *Service, c models.ChannelConnection, contact models.Contact) {
	t.Helper()
	var messages, receipts int64
	if err := s.DB.Model(&models.Message{}).Count(&messages).Error; err != nil || messages != 1 {
		t.Fatalf("private reply entered generic messages: %d %v", messages, err)
	}
	if err := s.DB.Model(&models.ChannelReceipt{}).Count(&receipts).Error; err != nil || receipts != 0 {
		t.Fatalf("protected polling mutated generic receipts: %d %v", receipts, err)
	}
	if err := s.DB.First(&c, "id=?", c.ID).Error; err != nil || (c.Cursor != "" || c.PollFailures != 0) {
		t.Fatalf("protected polling state changed: cursor=%q failures=%d err=%v", c.Cursor, c.PollFailures, err)
	}
	if err := s.DB.First(&contact, "id=?", contact.ID).Error; err != nil || contact.LastMessagePreview != "old preview" {
		t.Fatalf("private reply entered generic preview: %q %v", contact.LastMessagePreview, err)
	}
}
func TestProtectedHostGenericPollingDeniesBeforeProvider(t *testing.T) {
	s, p, c, contact := protectedHostPollFixture(t)
	if err := models.EnableProtectedHost(s.DB); err != nil {
		t.Fatal(err)
	}
	_ = s.PollDue(context.Background(), time.Now())
	if p.calls != 0 {
		t.Fatal("protected host still called generic SMS provider")
	}
	if err := s.PollConnection(context.Background(), c.ID); err == nil {
		t.Fatal("direct generic polling bypassed marker")
	}
	assertProtectedPollPreserved(t, s, c, contact)
}
func TestProtectedHostGenericPollingRechecksBeforePersistence(t *testing.T) {
	s, p, c, contact := protectedHostPollFixture(t)
	p.beforeReturn = func() {
		if err := models.EnableProtectedHost(s.DB); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PollConnection(context.Background(), c.ID); err == nil {
		t.Fatal("marker activated during provider poll was ignored")
	}
	if p.calls != 1 {
		t.Fatalf("wrong number of provider calls: %d", p.calls)
	}
	assertProtectedPollPreserved(t, s, c, contact)
}
func TestProtectedHostGenericPollingPreservesOrdinaryCompatibility(t *testing.T) {
	s, p, c, contact := protectedHostPollFixture(t)
	if err := s.PollConnection(context.Background(), c.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := s.DB.Model(&models.Message{}).Count(&count).Error; err != nil || count != 2 || p.calls != 1 {
		t.Fatalf("ordinary SMS reply failed: messages=%d calls=%d err=%v", count, p.calls, err)
	}
	if err := s.DB.First(&contact, "id=?", contact.ID).Error; err != nil || contact.LastMessagePreview != "PRIVATE-INBOUND-MARKER" {
		t.Fatalf("ordinary preview not updated: %v", err)
	}
}

func TestProtectedHostGenericPollingFencesFailureBackoff(t *testing.T) {
	s, p, c, contact := protectedHostPollFixture(t)
	p.failure = errors.New("synthetic network failure")
	p.beforeReturn = func() {
		if err := models.EnableProtectedHost(s.DB); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PollConnection(context.Background(), c.ID); err == nil {
		t.Fatal("provider failure unexpectedly succeeded")
	}
	assertProtectedPollPreserved(t, s, c, contact)
}

func TestProtectedHostGenericPollingRefusesMissingMarker(t *testing.T) {
	s, p, c, contact := protectedHostPollFixture(t)
	if err := s.DB.Exec(`DROP TABLE whatomate_protection_state`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.PollConnection(context.Background(), c.ID); err == nil || p.calls != 0 {
		t.Fatal("missing marker permitted generic provider access")
	}
	assertProtectedPollPreserved(t, s, c, contact)
}
