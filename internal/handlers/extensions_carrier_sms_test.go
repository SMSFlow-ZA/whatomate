package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/config"
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

type protectedSMSHostProvider struct {
	polls  int
	cursor string
}

func (p *protectedSMSHostProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "smsflow", Version: "fixture", APIVersion: 1, Channel: "sms"}
}
func (p *protectedSMSHostProvider) Validate(context.Context, channels.Connection) (channels.ConnectionInfo, error) {
	return channels.ConnectionInfo{}, nil
}
func (p *protectedSMSHostProvider) Send(context.Context, channels.Connection, channels.Message) (channels.Submission, error) {
	return channels.Submission{}, fmt.Errorf("unexpected send")
}
func (p *protectedSMSHostProvider) Reconcile(context.Context, channels.Connection, string) (channels.Submission, error) {
	return channels.Submission{}, nil
}
func (p *protectedSMSHostProvider) Poll(_ context.Context, c channels.Connection, cursor string) (channels.EventPage, error) {
	p.polls++
	p.cursor = cursor
	return channels.EventPage{NextCursor: "next-cursor", Events: []channels.Event{{ID: "receipt", Kind: "status", ClientMessageID: "intent:sms-fallback", ProviderMessageID: "provider", Status: channels.StatusDelivered, OccurredAt: time.Now()}, {ID: "reply", Kind: "inbound", Phone: "+27820000001", Text: "private synthetic text"}}}, nil
}
func TestProtectedSMSCurrentConnectionAndReceiptAdapter(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	base, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("carrier_sms_host_%d", time.Now().UnixNano())
	if _, e = base.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, e := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := db.DB()
	t.Cleanup(func() { raw.Close(); base.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); base.Close() })
	if e = db.AutoMigrate(&models.ChannelConnection{}); e != nil {
		t.Fatal(e)
	}
	org, id, wa := uuid.New(), uuid.New(), uuid.New()
	key := strings.Repeat("k", 32)
	sealed, e := crypto.Encrypt(`{"client_id":"synthetic","client_secret":"synthetic"}`, key)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	connection := models.ChannelConnection{BaseModel: models.BaseModel{ID: id}, OrganizationID: org, Name: "private synthetic", Provider: "smsflow", Enabled: true, MaxSegments: 1, Credentials: sealed, ValidatedAt: &now, Capabilities: models.JSONB{"text": true, "delivery_reports": true}}
	if e = db.Create(&connection).Error; e != nil {
		t.Fatal(e)
	}
	registry := channels.NewRegistry()
	provider := &protectedSMSHostProvider{}
	if e = registry.Register(provider); e != nil {
		t.Fatal(e)
	}
	a := &App{DB: db, Config: &config.Config{App: config.AppConfig{EncryptionKey: key}, Extensions: config.ExtensionsConfig{CarrierEnabled: true, CarrierDispatchEnabled: true, CarrierSMSBindings: []config.ExtensionCarrierSMSBinding{{OrganizationID: org.String(), ConnectionID: id.String(), WhatsAppAccountID: wa.String()}}}}, Channels: &channelapp.Service{DB: db, Providers: registry, ProtectedConnectionIDs: []uuid.UUID{id}}, ExtensionCarrier: &carrier.Service{Bindings: []carrier.Binding{{Tenant: org.String(), Account: wa.String(), PhoneID: "123", AppSecret: "secret"}}}}
	if e = a.initializeProtectedSMS(context.Background()); e != nil {
		t.Fatal(e)
	}
	page, e := a.pollProtectedSMS(context.Background(), org.String(), id.String(), "previous-cursor")
	if e != nil || len(page.Receipts) != 1 || page.Receipts[0].Outcome != "delivered" || page.Receipts[0].ProviderClientID != "intent:sms-fallback" || provider.cursor != "previous-cursor" {
		t.Fatal("receipt mapping", page, e)
	}
	if strings.Contains(page.Receipts[0].Evidence, "private") || page.Receipts[0].Account != id.String() {
		t.Fatal("private payload escaped status-only adapter")
	}
	a.Channels.StopDispatch = true
	if _, _, e = a.protectedSMSRuntime(context.Background(), org.String(), id.String(), true); e == nil {
		t.Fatal("stopped dispatch resolved transport")
	}
	if _, e = a.pollProtectedSMS(context.Background(), org.String(), id.String(), page.NextCursor); e != nil {
		t.Fatal("receipt polling must continue with dispatch stopped", e)
	}
	if _, _, e = a.protectedSMSRuntime(context.Background(), uuid.NewString(), id.String(), false); e == nil {
		t.Fatal("cross-organization account resolved")
	}
	if _, _, e = a.protectedSMSRuntime(context.Background(), org.String(), "malformed", false); e == nil {
		t.Fatal("malformed account resolved")
	}
	result, _, _ := a.ExtensionCarrier.SendSMSObserved(context.Background(), delivery.Message{ID: "intent:sms-fallback", Tenant: org.String(), Account: id.String(), Sender: "+27820000001", Channel: "sms", Text: delivery.MinimalSMSNotice})
	if result != delivery.Unknown {
		t.Fatal("missing employee authorizer accepted")
	}
	if e = db.Model(&connection).Update("enabled", false).Error; e != nil {
		t.Fatal(e)
	}
	if _, e = a.pollProtectedSMS(context.Background(), org.String(), id.String(), page.NextCursor); e == nil {
		t.Fatal("revoked connection used by poll")
	}
}
