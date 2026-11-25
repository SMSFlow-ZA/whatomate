package handlers

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type intakeBindingFixture struct {
	responsePolicyFixture
	contact *models.Contact
	session models.ChatbotSession
	flow    models.ChatbotFlow
	node    ChatNode
	mapping models.InboxIntakeMapping
	field   models.InboxFieldDefinition
}

func newIntakeBindingFixture(t *testing.T) intakeBindingFixture {
	t.Helper()
	f := newResponsePolicyFixture(t)
	f.a.Config = &config.Config{}
	f.a.Config.App.EncryptionKey = strings.Repeat("synthetic-intake-key-", 3)
	require.NoError(t, database.InstallInboxFieldStamps(f.db))
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	field := intakeField(t, f, "intake_reference", "case", "text", nil)
	mapping := models.InboxIntakeMapping{OrganizationID: f.org.ID, OwnerID: f.admin.ID, Name: "Synthetic intake", ConsentKey: "consent", Enabled: true, Fields: []models.InboxIntakeInput{{Input: "reference", DefinitionID: field.ID, Required: true}}}
	require.NoError(t, f.db.Create(&mapping).Error)
	node := ChatNode{ID: "intake", Type: ChatNodeWhatsAppFlow, Config: map[string]any{"intake_mapping_id": mapping.ID.String(), "flow_id": "synthetic-meta-flow"}}
	graph := ChatGraph{Version: 2, EntryNode: node.ID, Nodes: []ChatNode{node}}
	raw, err := json.Marshal(graph)
	require.NoError(t, err)
	var graphJSON models.JSONB
	require.NoError(t, json.Unmarshal(raw, &graphJSON))
	flow := models.ChatbotFlow{OrganizationID: f.org.ID, Name: "Synthetic intake flow", Channel: "whatsapp", IsEnabled: true, Graph: graphJSON}
	require.NoError(t, f.db.Create(&flow).Error)
	session := models.ChatbotSession{OrganizationID: f.org.ID, ContactID: contact.ID, WhatsAppAccount: f.account.Name, PhoneNumber: contact.PhoneNumber, Channel: "whatsapp", Status: models.SessionStatusActive, CurrentFlowID: &flow.ID, CurrentStep: node.ID}
	require.NoError(t, f.db.Create(&session).Error)
	return intakeBindingFixture{f, contact, session, flow, node, mapping, field}
}

func (f intakeBindingFixture) bind(t *testing.T) models.InboxIntakeBinding {
	t.Helper()
	binding, err := f.a.bindInboxIntake(*f.account, *f.contact, f.session, f.flow, f.node, f.mapping.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, f.db.First(&binding, "id=?", binding.ID).Error)
	return binding
}
func (f intakeBindingFixture) response(binding models.InboxIntakeBinding) inboxIntakeResponse {
	return inboxIntakeResponse{BindingID: binding.ID, OrganizationID: f.org.ID, AccountID: f.account.ID, ContactID: f.contact.ID, SessionID: f.session.ID, EventKey: uuid.New(), FlowToken: binding.ID.String(), Inputs: map[string]any{"consent": true, "reference": "Synthetic private answer"}}
}
func (f intakeBindingFixture) apply(t *testing.T, binding models.InboxIntakeBinding, before models.InboxCase) models.InboxCase {
	t.Helper()
	var c models.InboxCase
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		_, err := inboxWorkflowEnabled(tx, f.org.ID, true)
		if err != nil {
			return err
		}
		if err := (inboxThread{f.org.ID, f.account.ID, f.contact.ID}).lock(tx); err != nil {
			return err
		}
		c = before
		if c.ID == uuid.Nil || c.Status == models.InboxCaseResolved {
			c = newInboxCase(inboxThread{f.org.ID, f.account.ID, f.contact.ID}, binding.IssuedAt.Add(2*time.Second), nil, nil)
			if err := tx.Create(&c).Error; err != nil {
				return err
			}
		}
		return f.a.applyStagedInboxIntake(tx, c, before, f.session.ID, binding.IssuedAt.Add(2*time.Second))
	}))
	return c
}
func (f intakeBindingFixture) stored(t *testing.T, id uuid.UUID) models.InboxIntakeBinding {
	t.Helper()
	var row models.InboxIntakeBinding
	require.NoError(t, f.db.First(&row, "id=?", id).Error)
	return row
}

func TestInboxIntakeStagesEncryptedPartialAnswersAndAppliesOnceAtHandoff(t *testing.T) {
	f := newIntakeBindingFixture(t)
	flag := intakeField(t, f.responsePolicyFixture, "flag", "contact", "boolean", nil)
	amount := intakeField(t, f.responsePolicyFixture, "amount", "case", "number", nil)
	optional := intakeField(t, f.responsePolicyFixture, "optional", "case", "email", nil)
	f.mapping.Fields = append(f.mapping.Fields, models.InboxIntakeInput{Input: "flag", DefinitionID: flag.ID}, models.InboxIntakeInput{Input: "amount", DefinitionID: amount.ID}, models.InboxIntakeInput{Input: "optional", DefinitionID: optional.ID})
	require.NoError(t, f.db.Save(&f.mapping).Error)
	binding := f.bind(t)
	response := f.response(binding)
	response.Inputs["flag"], response.Inputs["amount"] = false, 0
	staged, err := f.a.receiveInboxIntake(response, binding.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, "staged", staged.State)
	require.NotEmpty(t, staged.EncryptedAnswers)
	require.NotContains(t, staged.EncryptedAnswers, "Synthetic private answer")
	public, err := json.Marshal(staged)
	require.NoError(t, err)
	require.NotContains(t, string(public), "Synthetic private answer")
	require.NotContains(t, string(public), "encrypted_answers")
	var count int64
	require.NoError(t, f.db.Model(&models.InboxCase{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count, "collecting an answer must not fabricate a human case")
	c := f.apply(t, binding, models.InboxCase{})
	finished := f.stored(t, binding.ID)
	require.Equal(t, "applied", finished.State)
	require.Empty(t, finished.EncryptedAnswers)
	require.Equal(t, &c.ID, finished.CaseID)
	var rows []models.InboxFieldValue
	require.NoError(t, f.db.Where("organization_id=?", f.org.ID).Order("definition_id").Find(&rows).Error)
	require.Len(t, rows, 3, "No and zero must be retained; optional empty answers are not writes")
	stamps := map[uuid.UUID]int64{}
	for _, row := range rows {
		stamp, err := intakeFieldStamp(f.db, f.org.ID, row.DefinitionID, row.SubjectID)
		require.NoError(t, err)
		stamps[row.DefinitionID] = stamp
	}
	// Duplicate callback and a second handoff application do not mutate a value.
	response.Inputs["reference"] = "A different duplicate payload"
	again, err := f.a.receiveInboxIntake(response, binding.IssuedAt.Add(3*time.Second))
	require.NoError(t, err)
	require.Equal(t, "applied", again.State)
	require.NoError(t, f.a.applyStagedInboxIntake(f.db, c, c, f.session.ID, binding.IssuedAt.Add(4*time.Second)))
	for _, row := range rows {
		stamp, err := intakeFieldStamp(f.db, f.org.ID, row.DefinitionID, row.SubjectID)
		require.NoError(t, err)
		require.Equal(t, stamps[row.DefinitionID], stamp)
	}
}

func TestInboxIntakeRejectsMismatchedAccountSessionAndOpaqueResponseIdentity(t *testing.T) {
	f := newIntakeBindingFixture(t)
	binding := f.bind(t)
	for _, change := range []func(*inboxIntakeResponse){func(r *inboxIntakeResponse) { r.AccountID = uuid.New() }, func(r *inboxIntakeResponse) { r.ContactID = uuid.New() }, func(r *inboxIntakeResponse) { r.SessionID = uuid.New() }, func(r *inboxIntakeResponse) { r.OrganizationID = uuid.New() }, func(r *inboxIntakeResponse) { r.FlowToken = "wrong" }} {
		r := f.response(binding)
		change(&r)
		_, err := f.a.receiveInboxIntake(r, binding.IssuedAt.Add(time.Second))
		require.Error(t, err)
	}
	require.Equal(t, "pending", f.stored(t, binding.ID).State)
	// Reusing the exact contact in another owned account grants no binding.
	other := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	r := f.response(binding)
	r.AccountID = other.ID
	_, err := f.a.receiveInboxIntake(r, binding.IssuedAt.Add(time.Second))
	require.Error(t, err)
	valid, err := f.a.receiveInboxIntake(f.response(binding), binding.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, "staged", valid.State)
}

func TestInboxIntakeRequiresExplicitConsentAndReportsInvalidAndMissingInputs(t *testing.T) {
	t.Run("consent", func(t *testing.T) {
		f := newIntakeBindingFixture(t)
		binding := f.bind(t)
		r := f.response(binding)
		delete(r.Inputs, "consent")
		result, err := f.a.receiveInboxIntake(r, binding.IssuedAt.Add(time.Second))
		require.NoError(t, err)
		require.Equal(t, "consent_required", result.State)
		require.Empty(t, result.EncryptedAnswers)
	})
	t.Run("partial", func(t *testing.T) {
		f := newIntakeBindingFixture(t)
		link := intakeField(t, f.responsePolicyFixture, "link", "case", "url", nil)
		missing := intakeField(t, f.responsePolicyFixture, "missing", "case", "text", nil)
		f.mapping.Fields = append(f.mapping.Fields, models.InboxIntakeInput{Input: "link", DefinitionID: link.ID}, models.InboxIntakeInput{Input: "missing", DefinitionID: missing.ID, Required: true})
		require.NoError(t, f.db.Save(&f.mapping).Error)
		binding := f.bind(t)
		r := f.response(binding)
		r.Inputs["link"] = "http://example.invalid/never-fetched"
		result, err := f.a.receiveInboxIntake(r, binding.IssuedAt.Add(time.Second))
		require.NoError(t, err)
		require.Equal(t, []models.InboxIntakeOutcome{{DefinitionID: f.field.ID, Status: "pending_handoff"}, {DefinitionID: link.ID, Status: "invalid_value"}, {DefinitionID: missing.ID, Status: "required_missing"}}, result.Outcomes)
		f.apply(t, binding, models.InboxCase{})
		require.Equal(t, "applied_with_warnings", f.stored(t, binding.ID).State)
	})
}

func TestInboxIntakeDoesNotOverwriteHumanEditAndClearOrArchivedDefinitions(t *testing.T) {
	f := newIntakeBindingFixture(t)
	contactField := intakeField(t, f.responsePolicyFixture, "contact_ref", "contact", "text", nil)
	f.mapping.Fields = append(f.mapping.Fields, models.InboxIntakeInput{Input: "contact_ref", DefinitionID: contactField.ID})
	require.NoError(t, f.db.Save(&f.mapping).Error)
	binding := f.bind(t)
	r := f.response(binding)
	r.Inputs["contact_ref"] = "Stale bot answer"
	_, err := f.a.receiveInboxIntake(r, binding.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	value := models.InboxFieldValue{OrganizationID: f.org.ID, DefinitionID: contactField.ID, SubjectID: f.contact.ID, Value: json.RawMessage(`"Newer human value"`)}
	require.NoError(t, f.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&value).Error)
	require.NoError(t, f.db.Delete(&value).Error)
	require.NoError(t, f.db.Model(&f.field).Update("archived_at", time.Now().UTC()).Error)
	f.apply(t, binding, models.InboxCase{})
	result := f.stored(t, binding.ID)
	require.Equal(t, "needs_review", result.State)
	require.Equal(t, []models.InboxIntakeOutcome{{DefinitionID: f.field.ID, Status: "definition_changed"}, {DefinitionID: contactField.ID, Status: "field_changed"}}, result.Outcomes)
	var count int64
	require.NoError(t, f.db.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntakeConcurrentDuplicateReceiptsAndApplicationsDoNotWriteTwice(t *testing.T) {
	f := newIntakeBindingFixture(t)
	binding := f.bind(t)
	response := f.response(binding)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := f.a.receiveInboxIntake(response, binding.IssuedAt.Add(time.Second)); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	c := f.apply(t, binding, models.InboxCase{})
	stamp, err := intakeFieldStamp(f.db, f.org.ID, f.field.ID, c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stamp)
	var receiptCount int64
	require.NoError(t, f.db.Model(&models.InboxIntakeBinding{}).Where("organization_id=? AND response_key=?", f.org.ID, response.EventKey).Count(&receiptCount).Error)
	require.EqualValues(t, 1, receiptCount)
}

func TestInboxIntakeCannotApplyToALaterCaseOrAfterMappingPermissionRevocation(t *testing.T) {
	t.Run("later case", func(t *testing.T) {
		f := newIntakeBindingFixture(t)
		binding := f.bind(t)
		_, err := f.a.receiveInboxIntake(f.response(binding), binding.IssuedAt.Add(time.Second))
		require.NoError(t, err)
		first := newInboxCase(inboxThread{f.org.ID, f.account.ID, f.contact.ID}, binding.IssuedAt.Add(time.Second), nil, nil)
		first.Status = models.InboxCaseResolved
		require.NoError(t, f.db.Create(&first).Error)
		f.apply(t, binding, first)
		require.Equal(t, "case_changed", f.stored(t, binding.ID).State)
	})
	t.Run("revoked", func(t *testing.T) {
		f := newIntakeBindingFixture(t)
		binding := f.bind(t)
		_, err := f.a.receiveInboxIntake(f.response(binding), binding.IssuedAt.Add(time.Second))
		require.NoError(t, err)
		require.NoError(t, f.db.Model(f.admin).Update("is_active", false).Error)
		f.apply(t, binding, models.InboxCase{})
		require.Equal(t, "permission_required", f.stored(t, binding.ID).State)
	})
}

func TestInboxIntakeCaseVersionsPermitOnlyRecordedInboundIncrements(t *testing.T) {
	f := newIntakeBindingFixture(t)
	original := intakeCase(t, f.responsePolicyFixture, f.account.ID, f.contact.ID)
	binding := f.bind(t)
	require.Equal(t, &original.ID, binding.PreviousCaseID)
	_, err := f.a.receiveInboxIntake(f.response(binding), binding.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	f.a.recordInboxInbound(f.account, f.contact, "synthetic-after-binding", binding.IssuedAt.Add(time.Second), nil)
	var c models.InboxCase
	require.NoError(t, f.db.First(&c, "id=?", *binding.PreviousCaseID).Error)
	allowed, err := intakeCaseUnchanged(f.db, binding, c)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, f.db.Model(&c).Update("version", gorm.Expr("version+1")).Error)
	require.NoError(t, f.db.First(&c, "id=?", c.ID).Error)
	allowed, err = intakeCaseUnchanged(f.db, binding, c)
	require.NoError(t, err)
	require.False(t, allowed)
	f.apply(t, binding, c)
	require.Equal(t, "case_changed", f.stored(t, binding.ID).State)
}

func TestInboxIntakeLaterConsentRefusalBlocksOlderStagingButNotANewGrant(t *testing.T) {
	f := newIntakeBindingFixture(t)
	first := f.bind(t)
	_, err := f.a.receiveInboxIntake(f.response(first), first.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	refusal := f.bind(t)
	refused := f.response(refusal)
	refused.Inputs["consent"] = false
	_, err = f.a.receiveInboxIntake(refused, refusal.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	newGrant := f.bind(t)
	granted := f.response(newGrant)
	granted.Inputs["reference"] = "Newly consented answer"
	_, err = f.a.receiveInboxIntake(granted, newGrant.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	c := f.apply(t, newGrant, models.InboxCase{})
	old := f.stored(t, first.ID)
	require.Equal(t, "consent_required", old.State)
	require.Empty(t, old.EncryptedAnswers)
	require.Equal(t, "consent_required", f.stored(t, refusal.ID).State)
	require.Equal(t, "applied", f.stored(t, newGrant.ID).State)
	var value models.InboxFieldValue
	require.NoError(t, f.db.First(&value, "organization_id=? AND definition_id=? AND subject_id=?", f.org.ID, f.field.ID, c.ID).Error)
	require.JSONEq(t, `"Newly consented answer"`, string(value.Value))
	stamp, err := intakeFieldStamp(f.db, f.org.ID, f.field.ID, c.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stamp)
}
