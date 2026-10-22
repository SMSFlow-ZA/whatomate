package channelapp

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type isolationProvider struct {
	testProtectedProvider
	polled, validated, reconciled []string
}

func (p *isolationProvider) Poll(_ context.Context, c channels.Connection, _ string) (channels.EventPage, error) {
	p.polled = append(p.polled, c.ID)
	return channels.EventPage{}, nil
}
func (p *isolationProvider) Validate(_ context.Context, c channels.Connection) (channels.ConnectionInfo, error) {
	p.validated = append(p.validated, c.ID)
	return channels.ConnectionInfo{Capabilities: channels.Capabilities{Text: true, DeliveryReports: true}}, nil
}
func (p *isolationProvider) Reconcile(_ context.Context, c channels.Connection, _ string) (channels.Submission, error) {
	p.reconciled = append(p.reconciled, c.ID)
	return channels.Submission{Status: channels.StatusUnknown}, nil
}

// This fixture owns only its fresh schema. It never resets shared test data.
func TestProtectedConnectionWorkersKeepOrdinaryWorkMoving(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	base, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("channel_isolation_%d", time.Now().UnixNano())
	if _, e = base.Exec(`CREATE SCHEMA ` + schema); e != nil {
		base.Close()
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := db.DB()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { raw.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	if e = db.AutoMigrate(&models.ChannelConnection{}, &models.ChannelAttempt{}); e != nil {
		t.Fatal(e)
	}
	if e = models.InstallProtectedHostGuard(db); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	key := strings.Repeat("k", 32)
	encrypted, e := crypto.Encrypt(`{"client_id":"synthetic","client_secret":"synthetic"}`, key)
	if e != nil {
		t.Fatal(e)
	}
	p := &isolationProvider{}
	registry := channels.NewRegistry()
	if e = registry.Register(p); e != nil {
		t.Fatal(e)
	}
	s := Service{DB: db, Providers: registry, EncryptionKey: key}
	org := uuid.New()
	ordinary := uuid.New()
	for i := 0; i < 22; i++ {
		id := uuid.New()
		if i == 21 {
			id = ordinary
		} else {
			s.ProtectedConnectionIDs = append(s.ProtectedConnectionIDs, id)
		}
		conn := models.ChannelConnection{BaseModel: models.BaseModel{ID: id}, OrganizationID: org, Name: "synthetic", Provider: "test-private", Enabled: true, Credentials: encrypted, Capabilities: models.JSONB{}, ValidatedAt: &now, NextPollAt: now.Add(time.Duration(i-30) * time.Hour)}
		if e = db.Create(&conn).Error; e != nil {
			t.Fatal(e)
		}
		attempt := models.ChannelAttempt{OrganizationID: org, MessageID: uuid.New(), LogicalMessageID: uuid.New(), ConnectionID: &id, Channel: "sms", Status: "unknown"}
		if e = db.Create(&attempt).Error; e != nil {
			t.Fatal(e)
		}
	}
	ctx := context.Background()
	if e = s.PollDue(ctx, now); e != nil {
		t.Fatal(e)
	}
	if e = s.RefreshBalances(ctx, now); e != nil {
		t.Fatal(e)
	}
	if e = s.ReconcileUnknown(ctx); e != nil {
		t.Fatal(e)
	}
	private := s.ProtectedConnectionIDs[0]
	expired := now.Add(-time.Minute)
	for _, state := range []string{"queued", "sending"} {
		attempt := models.ChannelAttempt{OrganizationID: org, MessageID: uuid.New(), LogicalMessageID: uuid.New(), ConnectionID: &private, Channel: "sms", Status: state, DueAt: &expired, LeaseUntil: &expired}
		if e = db.Create(&attempt).Error; e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Dispatch(ctx, now); e != nil {
		t.Fatal(e)
	}
	var preserved int64
	if e = db.Model(&models.ChannelAttempt{}).Where("connection_id = ? AND status IN ?", private, []string{"queued", "sending"}).Count(&preserved).Error; e != nil || preserved != 2 {
		t.Fatal("generic dispatch changed protected work", preserved, e)
	}
	for _, calls := range [][]string{p.polled, p.validated, p.reconciled} {
		if len(calls) != 1 || calls[0] != ordinary.String() {
			t.Fatal("protected connections consumed work or ordinary work starved", calls)
		}
	}
	var touched int64
	if e = db.Model(&models.ChannelAttempt{}).Where("connection_id IN ? AND reconcile_failures<>0", s.ProtectedConnectionIDs).Count(&touched).Error; e != nil || touched != 0 {
		t.Fatal("generic reconciliation mutated protected attempts", touched, e)
	}
	// Neither polling nor balance/reconciliation may materialize generic contact or transcript tables.
	var leaked bool
	if e = db.Raw(`SELECT to_regclass('contacts') IS NOT NULL OR to_regclass('messages') IS NOT NULL`).Scan(&leaked).Error; e != nil || leaked {
		t.Fatal("generic transcript materialized", leaked, e)
	}
}
