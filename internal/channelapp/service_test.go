package channelapp_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

type provider struct {
	sends            atomic.Int32
	unknown          bool
	page             channels.EventPage
	validateError    bool
	blockSend        <-chan struct{}
	polls            atomic.Int32
	permanentUnknown map[string]bool
	nonReplayable    bool
}

func (*provider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "fake", Name: "Fake SMS", Version: "0.1.0", APIVersion: 1, Channel: "sms"}
}
func (p *provider) Validate(context.Context, channels.Connection) (channels.ConnectionInfo, error) {
	if p.validateError {
		return channels.ConnectionInfo{}, &channels.ProviderError{Kind: "unavailable"}
	}
	return channels.ConnectionInfo{Capabilities: channels.Capabilities{Text: true, Inbound: true, DeliveryReports: true, Reconciliation: !p.nonReplayable, IdempotentSubmission: !p.nonReplayable}}, nil
}

func TestNonReplayableTransportIsFencedAcrossConcurrentDispatchAndRestart(t *testing.T) {
	svc, p, org, contact, _ := setup(t)
	p.nonReplayable = true
	p.unknown = true
	ctx := context.Background()
	c, err := svc.CreateConnection(ctx, org.ID, "Existing SMPP", "fake", channels.Credentials{"secret": "private"}, 3)
	require.NoError(t, err)
	require.Equal(t, false, c.Capabilities["idempotent_submission"])
	logical := uuid.New()
	message, err := svc.EnqueueSMSWithID(ctx, org.ID, contact.ID, c.ID, logical, "One charge only", nil, true, "api")
	require.NoError(t, err)
	again, err := svc.EnqueueSMSWithID(ctx, org.ID, contact.ID, c.ID, logical, "One charge only", nil, true, "api")
	require.NoError(t, err)
	require.Equal(t, message.ID, again.ID)
	_, err = svc.EnqueueSMSWithID(ctx, org.ID, contact.ID, c.ID, logical, "Changed content", nil, true, "api")
	require.Error(t, err)
	var workers sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		workers.Add(1)
		go func() { defer workers.Done(); errors <- svc.Dispatch(ctx, time.Now().Add(time.Second)) }()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, p.sends.Load(), "one carrier submission and charge despite concurrent workers")
	var attempt models.ChannelAttempt
	require.NoError(t, svc.DB.First(&attempt, "message_id = ?", logical).Error)
	require.Equal(t, "unknown", attempt.Status)
	p.permanentUnknown = map[string]bool{attempt.ID.String(): true}
	restarted := &channelapp.Service{DB: svc.DB, Providers: svc.Providers, EncryptionKey: svc.EncryptionKey}
	for range 3 {
		require.NoError(t, restarted.Dispatch(ctx, time.Now().Add(5*time.Minute)))
		require.NoError(t, restarted.ReconcileUnknown(ctx))
	}
	require.EqualValues(t, 1, p.sends.Load(), "unknown work must not be submitted again after restart")
	p.page = channels.EventPage{NextCursor: "receipt-1", Events: []channels.Event{{ID: "delivered", Kind: "status", ClientMessageID: attempt.ID.String(), ProviderMessageID: "original", Status: "delivered"}}}
	require.NoError(t, restarted.PollConnection(ctx, c.ID))
	require.NoError(t, svc.DB.First(&attempt, "id = ?", attempt.ID).Error)
	require.Equal(t, "delivered", attempt.Status)
	require.EqualValues(t, 1, p.sends.Load())
}

func TestFailedRemoteValidationKeepsRecoverableConnectionIdentity(t *testing.T) {
	svc, p, org, _, _ := setup(t)
	p.validateError = true
	_, err := svc.CreateConnection(context.Background(), org.ID, "Recoverable", "fake", channels.Credentials{"secret": "private"}, 3)
	require.Error(t, err)
	var c models.ChannelConnection
	require.NoError(t, svc.DB.First(&c, "organization_id = ? AND name = ?", org.ID, "Recoverable").Error)
	require.False(t, c.Enabled)
	require.NotContains(t, c.Credentials, "private")
	require.Equal(t, "validation_required", c.ErrorCode)
}

func TestDispatchSkipsLockedConnectionBeforeClaimingAttempts(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	_, err := svc.EnqueueSMS(context.Background(), org.ID, contact.ID, c.ID, "Hello", nil, true, "inbox")
	require.NoError(t, err)
	tx := svc.DB.Begin()
	defer func() {
		tx.Rollback()
		svc.DB.Model(&models.ChannelAttempt{}).Where("connection_id = ?", c.ID).Update("status", "cancelled")
	}()
	var locked models.ChannelConnection
	require.NoError(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", c.ID).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Second)), "dispatch must skip the connection locked by settings or polling, without locking its attempts first")
	require.Zero(t, p.sends.Load())
}

func TestSMSDispatchRechecksCurrentContactAssignment(t *testing.T) {
	svc, p, org, contact, c := setup(t)

	role := testutil.CreateTestRoleWithKeys(t, svc.DB, org.ID, "Assigned agent", []string{"chat:read", "chat:write"})
	user := testutil.CreateTestUser(t, svc.DB, org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, svc.DB.Model(&contact).Update("assigned_user_id", user.ID).Error)
	msg, err := svc.EnqueueSMS(context.Background(), org.ID, contact.ID, c.ID, "Hello", &user.ID, true, "inbox")
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&contact).Update("assigned_user_id", nil).Error)
	require.NoError(t, svc.Dispatch(context.Background(), time.Now().Add(time.Second)))
	require.Zero(t, p.sends.Load())
	var attempt models.ChannelAttempt
	require.NoError(t, svc.DB.First(&attempt, "message_id = ?", msg.ID).Error)
	require.Equal(t, "cancelled", attempt.Status)
}
func (p *provider) Send(ctx context.Context, _ channels.Connection, m channels.Message) (channels.Submission, error) {
	p.sends.Add(1)
	if p.blockSend != nil {
		select {
		case <-ctx.Done():
			return channels.Submission{}, ctx.Err()
		case <-p.blockSend:
		}
	}
	if p.unknown {
		return channels.Submission{}, &channels.ProviderError{Kind: "unknown"}
	}
	return channels.Submission{ClientMessageID: m.ClientMessageID, ProviderMessageID: "provider-" + m.ClientMessageID, Status: "submitted"}, nil
}
func (p *provider) Reconcile(_ context.Context, _ channels.Connection, id string) (channels.Submission, error) {
	if p.permanentUnknown[id] {
		return channels.Submission{ClientMessageID: id, Status: "unknown"}, nil
	}
	return channels.Submission{ClientMessageID: id, ProviderMessageID: "provider-" + id, Status: "submitted"}, nil
}

func TestReconciliationEventuallyVisitsBeyondPermanentUnknownBatch(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	p.permanentUnknown = map[string]bool{}
	var last models.ChannelAttempt
	for i := 0; i < 21; i++ {
		last = models.ChannelAttempt{}
		m, err := svc.EnqueueSMS(context.Background(), org.ID, contact.ID, c.ID, "Hello", nil, true, "api")
		require.NoError(t, err)
		require.NoError(t, svc.DB.First(&last, "message_id = ?", m.ID).Error)
		require.NoError(t, svc.DB.Model(&last).Updates(map[string]any{"status": "unknown", "updated_at": time.Now().Add(time.Duration(i-30) * time.Minute)}).Error)
		if i < 20 {
			p.permanentUnknown[last.ID.String()] = true
		}
	}
	require.NoError(t, svc.ReconcileUnknown(context.Background()))
	require.NoError(t, svc.ReconcileUnknown(context.Background()))
	require.NoError(t, svc.DB.First(&last, "id = ?", last.ID).Error)
	require.Equal(t, "submitted", last.Status)
	require.Zero(t, p.sends.Load())
}

func TestRestoreHoldReconcilesExpiredInflightWithoutSendingQueuedWork(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	m, err := svc.EnqueueSMS(context.Background(), org.ID, contact.ID, c.ID, "Inflight", nil, true, "api")
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&models.ChannelAttempt{}).Where("message_id = ?", m.ID).Updates(map[string]any{"status": "sending", "lease_until": time.Now().Add(-time.Hour)}).Error)
	queued, err := svc.EnqueueSMS(context.Background(), org.ID, contact.ID, c.ID, "Queued", nil, true, "api")
	require.NoError(t, err)
	svc.StopDispatch = true
	require.NoError(t, svc.ReconcileUnknown(context.Background()))
	require.NoError(t, svc.Dispatch(context.Background(), time.Now()))
	var inflight, pending models.ChannelAttempt
	require.NoError(t, svc.DB.First(&inflight, "message_id = ?", m.ID).Error)
	require.NoError(t, svc.DB.First(&pending, "message_id = ?", queued.ID).Error)
	require.Equal(t, "submitted", inflight.Status)
	require.Equal(t, "queued", pending.Status)
	require.Zero(t, p.sends.Load())
}
func (p *provider) Poll(context.Context, channels.Connection, string) (channels.EventPage, error) {
	p.polls.Add(1)
	return p.page, nil
}

func TestWorkerPollsWhileSubmissionIsSlow(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	block := make(chan struct{})
	p.blockSend = block
	_, err := svc.EnqueueSMS(context.Background(), org.ID, contact.ID, c.ID, "Slow", nil, true, "inbox")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx, 5*time.Millisecond, nil) }()
	defer func() { cancel(); close(block); <-done }()
	require.Eventually(t, func() bool { return p.sends.Load() > 0 }, time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return p.polls.Load() > 0 }, time.Second, time.Millisecond, "polling must not wait for a slow submission")
}

func setup(t *testing.T) (*channelapp.Service, *provider, models.Organization, models.Contact, models.ChannelConnection) {
	t.Helper()
	db := testutil.SetupTestDB(t)
	require.NoError(t, channelapp.Migrate(db))
	org := testutil.CreateTestOrganization(t, db)
	// Workers scan the whole instance database. Do not leave a previous test's
	// intentionally held queue eligible for a later test's fake provider.
	t.Cleanup(func() {
		db.Where("organization_id = ?", org.ID).Delete(&models.ChannelAttempt{})
		db.Model(&models.ChannelConnection{}).Where("organization_id = ?", org.ID).Update("enabled", false)
	})
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(contact).Update("phone_number", "27820000000").Error)
	contact.PhoneNumber = "27820000000"
	p := &provider{}
	registry := channels.NewRegistry()
	require.NoError(t, registry.Register(p))
	svc := &channelapp.Service{DB: db, Providers: registry, EncryptionKey: strings.Repeat("k", 32)}
	c, err := svc.CreateConnection(context.Background(), org.ID, "SMS", "fake", channels.Credentials{"secret": "must-be-encrypted"}, 3)
	require.NoError(t, err)
	return svc, p, *org, *contact, *c
}

func TestDurableSMSClaimIsExclusiveAndCredentialsAreEncrypted(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	ctx := context.Background()
	var stored models.ChannelConnection
	require.NoError(t, svc.DB.First(&stored, "id = ?", c.ID).Error)
	require.NotContains(t, stored.Credentials, "must-be-encrypted")
	msg, err := svc.EnqueueSMS(ctx, org.ID, contact.ID, c.ID, "Hello", nil, true, "inbox")
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Second))) }()
	}
	wg.Wait()
	require.EqualValues(t, 1, p.sends.Load())
	var attempts []models.ChannelAttempt
	require.NoError(t, svc.DB.Where("message_id = ?", msg.ID).Find(&attempts).Error)
	require.Len(t, attempts, 1)
	require.Equal(t, "submitted", attempts[0].Status)
}

func TestSMSCampaignPauseCancelAndReports(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	ctx := context.Background()
	user := testutil.CreateTestUser(t, svc.DB, org.ID, testutil.WithSuperAdmin())
	campaign := models.BulkMessageCampaign{OrganizationID: org.ID, Channel: "sms", ChannelConnectionID: &c.ID, Name: "Controlled campaign", CreatedBy: user.ID, Status: models.CampaignStatusPaused, TotalRecipients: 1}
	require.NoError(t, svc.DB.Create(&campaign).Error)
	msg, err := svc.EnqueueSMS(ctx, org.ID, contact.ID, c.ID, "Hello", &user.ID, true, "campaign")
	require.NoError(t, err)
	recipient := models.BulkMessageRecipient{CampaignID: campaign.ID, PhoneNumber: contact.PhoneNumber, MessageID: &msg.ID, Status: "queued"}
	require.NoError(t, svc.DB.Create(&recipient).Error)
	now := time.Now().Add(time.Second)
	require.NoError(t, svc.Dispatch(ctx, now))
	require.Zero(t, p.sends.Load(), "paused campaign must not dispatch")
	require.NoError(t, svc.DB.Model(&campaign).Update("status", models.CampaignStatusProcessing).Error)
	require.NoError(t, svc.Dispatch(ctx, now.Add(time.Minute)))
	require.EqualValues(t, 1, p.sends.Load())
	require.NoError(t, svc.DB.First(&recipient, "id = ?", recipient.ID).Error)
	require.Equal(t, models.MessageStatusSent, recipient.Status)
	var attempt models.ChannelAttempt
	require.NoError(t, svc.DB.First(&attempt, "message_id = ?", msg.ID).Error)
	p.page = channels.EventPage{NextCursor: "done", Events: []channels.Event{{ID: "delivered", Kind: "status", ClientMessageID: attempt.ID.String(), Status: "delivered"}}}
	require.NoError(t, svc.PollConnection(ctx, c.ID))
	require.NoError(t, svc.PollConnection(ctx, c.ID))
	require.NoError(t, svc.DB.First(&campaign, "id = ?", campaign.ID).Error)
	require.Equal(t, 1, campaign.SentCount)
	require.Equal(t, 1, campaign.DeliveredCount)
	require.Equal(t, 0, campaign.ReadCount)
	msg2, err := svc.EnqueueSMS(ctx, org.ID, contact.ID, c.ID, "Must cancel", &user.ID, true, "campaign")
	require.NoError(t, err)
	recipient.ID = uuid.New()
	recipient.MessageID = &msg2.ID
	recipient.Status = "queued"
	require.NoError(t, svc.DB.Create(&recipient).Error)
	require.NoError(t, svc.DB.Model(&campaign).Update("status", models.CampaignStatusCancelled).Error)
	require.NoError(t, svc.Dispatch(ctx, now.Add(2*time.Minute)))
	require.EqualValues(t, 1, p.sends.Load())
	attempt = models.ChannelAttempt{}
	require.NoError(t, svc.DB.First(&attempt, "message_id = ?", msg2.ID).Error)
	require.Equal(t, "cancelled", attempt.Status)
}

func TestFallbackCancelledByDeliveryAndOnlyEligibleFailuresSend(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	ctx := context.Background()
	wa := models.Message{OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "real-wa", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Hello", Status: models.MessageStatusSent}
	require.NoError(t, svc.DB.Create(&wa).Error)
	require.NoError(t, svc.AttachFallback(ctx, &wa, c.ID, "", true, 10*time.Minute))
	require.NoError(t, svc.RecordWhatsApp(ctx, wa.ID, "delivered", 0))
	require.NoError(t, svc.RecordWhatsApp(ctx, wa.ID, "failed", 131026))
	require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Hour)))
	require.EqualValues(t, 0, p.sends.Load())
	wa.ID = uuid.New()
	require.NoError(t, svc.DB.Create(&wa).Error)
	require.NoError(t, svc.AttachFallback(ctx, &wa, c.ID, "", true, 0))
	require.NoError(t, svc.RecordWhatsApp(ctx, wa.ID, "failed", 190))
	require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Hour)))
	require.EqualValues(t, 0, p.sends.Load())
	require.NoError(t, svc.RecordWhatsApp(ctx, wa.ID, "failed", 131026))
	require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Second)))
	require.EqualValues(t, 1, p.sends.Load())
	require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Hour)))
	require.EqualValues(t, 1, p.sends.Load())
}

func TestSuppressionIsolationAndUnknownSubmissionCannotResend(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	ctx := context.Background()
	_, err := svc.EnqueueSMS(ctx, uuid.New(), contact.ID, c.ID, "Hello", nil, true, "api")
	require.Error(t, err)
	_, err = svc.EnqueueSMS(ctx, org.ID, contact.ID, c.ID, "Hello", nil, false, "api")
	require.Error(t, err)
	_, err = svc.EnqueueSMS(ctx, org.ID, contact.ID, c.ID, strings.Repeat("A", 460), nil, true, "api")
	require.Error(t, err)
	msg, err := svc.EnqueueSMS(ctx, org.ID, contact.ID, c.ID, "Hello", nil, true, "api")
	require.NoError(t, err)
	p.unknown = true
	require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Second)))
	require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Hour)))
	require.EqualValues(t, 1, p.sends.Load())
	require.NoError(t, svc.DB.Model(&models.ChannelAttempt{}).Where("message_id = ?", msg.ID).Updates(map[string]any{"status": "sending", "lease_until": time.Now().Add(-time.Hour)}).Error)
	require.NoError(t, svc.Dispatch(ctx, time.Now()))
	require.EqualValues(t, 1, p.sends.Load(), "recovered in-flight attempt must reconcile, never resend")
	_, err = svc.EnqueueSMS(ctx, org.ID, contact.ID, c.ID, "Other", nil, true, "inbox")
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&models.Contact{}).Where("id = ?", contact.ID).Update("sms_opt_out", true).Error)
	require.NoError(t, svc.Dispatch(ctx, time.Now().Add(time.Second)))
	require.EqualValues(t, 1, p.sends.Load(), "suppression rechecked at dispatch")
}

func TestInboundReplayKeepsWhatsAppWindowAndCommitsCursorWithEvents(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	ctx := context.Background()
	msg, err := svc.EnqueueSMS(ctx, org.ID, contact.ID, c.ID, "Hello", nil, true, "api")
	require.NoError(t, err)
	var attempt models.ChannelAttempt
	require.NoError(t, svc.DB.First(&attempt, "message_id = ?", msg.ID).Error)
	p.page = channels.EventPage{NextCursor: "2", Events: []channels.Event{{ID: "reply-1", Kind: "inbound", ClientMessageID: attempt.ID.String(), Phone: "+27820000000", Text: "STOP", OptOut: true, OccurredAt: time.Now()}}}
	require.NoError(t, svc.PollConnection(ctx, c.ID))
	require.NoError(t, svc.PollConnection(ctx, c.ID))
	var count int64
	require.NoError(t, svc.DB.Model(&models.Message{}).Where("contact_id = ? AND direction = ?", contact.ID, models.DirectionIncoming).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, svc.DB.First(&contact, "id = ?", contact.ID).Error)
	require.Nil(t, contact.LastInboundAt)
	require.NotNil(t, contact.SMSLastInboundAt)
	require.True(t, contact.SMSOptOut)
	require.NoError(t, svc.DB.First(&c, "id = ?", c.ID).Error)
	require.Equal(t, "2", c.Cursor)
}

func TestNonDeliveryErrorsCannotBecomeTimeoutFallback(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	wa := models.Message{OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "wa", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Hello", Status: models.MessageStatusPending}
	require.NoError(t, svc.DB.Create(&wa).Error)
	require.NoError(t, svc.AttachFallback(context.Background(), &wa, c.ID, "", true, 10*time.Minute))
	require.NoError(t, svc.RecordWhatsApp(context.Background(), wa.ID, "failed", 190))
	require.NoError(t, svc.Dispatch(context.Background(), time.Now().Add(time.Hour)))
	require.EqualValues(t, 0, p.sends.Load(), "invalid WhatsApp credentials must not trigger SMS even with timeout enabled")
}

func TestTimeoutDoesNotFallbackAnUnconfirmedWhatsAppSubmission(t *testing.T) {
	svc, p, org, contact, c := setup(t)
	wa := models.Message{OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: "wa", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: "Hello", Status: models.MessageStatusPending}
	require.NoError(t, svc.DB.Create(&wa).Error)
	require.NoError(t, svc.AttachFallback(context.Background(), &wa, c.ID, "", true, 10*time.Minute))
	require.NoError(t, svc.Dispatch(context.Background(), time.Now().Add(time.Hour)))
	require.EqualValues(t, 0, p.sends.Load())
}

func TestStableSMSJobIdentifierCannotCreateAnotherLogicalMessage(t *testing.T) {
	svc, _, org, contact, c := setup(t)
	id := uuid.New()
	first, err := svc.EnqueueSMSWithID(context.Background(), org.ID, contact.ID, c.ID, id, "Hello", nil, true, "campaign")
	require.NoError(t, err)
	replayed, err := svc.EnqueueSMSWithID(context.Background(), org.ID, contact.ID, c.ID, id, "Hello", nil, true, "campaign")
	require.NoError(t, err)
	require.Equal(t, first.ID, replayed.ID)
	_, err = svc.EnqueueSMSWithID(context.Background(), org.ID, contact.ID, c.ID, id, "Different", nil, true, "campaign")
	require.Error(t, err)
	var count int64
	require.NoError(t, svc.DB.Model(&models.ChannelAttempt{}).Where("message_id = ?", id).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
