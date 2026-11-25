package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxIntakeGraphBindsPromptButtonsAndFormThenAppliesAtActualHandoff(t *testing.T) {
	for _, kind := range []ChatNodeType{ChatNodePrompt, ChatNodeButtons, ChatNodeWhatsAppFlow} {
		t.Run(string(kind), func(t *testing.T) {
			f := newIntakeBindingFixture(t)
			provider := newProcessorTestApp(t) // HTTP fake only, never Meta.
			f.a.WhatsApp = provider.WhatsApp
			f.a.HTTPClient = provider.HTTPClient
			f.a.Redis = provider.Redis
			f.node.Type = kind
			f.node.Config["body"] = "Synthetic intake prompt"
			if kind != ChatNodeWhatsAppFlow {
				f.mapping.Fields[0].Input = "answer"
				require.NoError(t, f.db.Save(&f.mapping).Error)
			}
			if kind == ChatNodeButtons {
				f.node.Config["buttons"] = []any{map[string]any{"id": "reference", "title": "Reference", "type": "reply"}}
			}
			graph := ChatGraph{Version: 2, EntryNode: f.node.ID, Nodes: []ChatNode{f.node, {ID: "handoff", Type: ChatNodeTransfer, Config: map[string]any{}}}, Edges: []ChatEdge{{From: f.node.ID, To: "handoff", Condition: "default"}}}
			if kind == ChatNodeButtons {
				graph.Edges[0].Condition = "button:reference"
			}
			raw, err := json.Marshal(graph)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &f.flow.Graph))
			require.NoError(t, f.db.Save(&f.flow).Error)
			require.NoError(t, validateChatIntakeGraph(f.db, f.org.ID, f.admin.ID, "whatsapp", f.flow.Graph))
			f.session.CurrentStep = ""
			f.session.SessionData = models.JSONB{"consent": true}
			require.NoError(t, f.db.Save(&f.session).Error)
			require.NoError(t, f.a.runChatGraph(f.account, f.contact, &f.session, &f.flow, "trigger", "", nil))
			var binding models.InboxIntakeBinding
			require.NoError(t, f.db.Where("organization_id=? AND session_id=? AND state='pending'", f.org.ID, f.session.ID).First(&binding).Error)
			var outbound models.Message
			require.NoError(t, f.db.First(&outbound, "id=? AND organization_id=?", binding.ID, f.org.ID).Error)
			require.NotEmpty(t, outbound.WhatsAppMessageID)
			require.NoError(t, f.db.First(&f.session, "id=?", f.session.ID).Error)
			f.session.IntakeReplyToWAMID = outbound.WhatsAppMessageID
			event := uuid.New()
			f.session.ChannelInboundMessageID = &event
			text, button := "Synthetic private answer", ""
			var form map[string]any
			if kind == ChatNodeButtons {
				text, button = "Reference", "reference"
			}
			if kind == ChatNodeWhatsAppFlow {
				text = "Form reply"
				form = map[string]any{"flow_token": binding.ID.String(), "reference": "Synthetic private answer", "consent": true}
			}
			require.NoError(t, f.a.runChatGraph(f.account, f.contact, &f.session, &f.flow, text, button, form))
			result := f.stored(t, binding.ID)
			require.Equal(t, "applied", result.State)
			require.Empty(t, result.EncryptedAnswers)
			require.NotNil(t, result.CaseID)
			var value models.InboxFieldValue
			require.NoError(t, f.db.First(&value, "organization_id=? AND definition_id=? AND subject_id=?", f.org.ID, f.field.ID, *result.CaseID).Error)
			expected := `"Synthetic private answer"`
			if kind == ChatNodeButtons {
				expected = `"reference"`
			}
			require.JSONEq(t, expected, string(value.Value))
			require.NoError(t, f.db.First(&f.session, "id=?", f.session.ID).Error)
			require.NotContains(t, f.session.SessionData, "reference", "mapped form payload is not copied into general session variables")
			var transfer models.AgentTransfer
			require.NoError(t, f.db.Where("organization_id=? AND contact_id=?", f.org.ID, f.contact.ID).First(&transfer).Error)
			require.Equal(t, &f.session.ID, transfer.InboxIntakeSessionID)
		})
	}
}

func TestInboxIntakePromptRequiresExactQuotedMessageAndNeverBorrowsAnotherAccount(t *testing.T) {
	f := newIntakeBindingFixture(t)
	f.node.Type = ChatNodePrompt
	f.node.Config["body"] = "Synthetic prompt"
	f.mapping.Fields[0].Input = "answer"
	require.NoError(t, f.db.Save(&f.mapping).Error)
	graph := ChatGraph{Version: 2, EntryNode: f.node.ID, Nodes: []ChatNode{f.node}}
	raw, err := json.Marshal(graph)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &f.flow.Graph))
	require.NoError(t, f.db.Save(&f.flow).Error)
	binding := f.bind(t)
	r := f.response(binding)
	r.Inputs = map[string]any{"answer": "Synthetic reply", "consent": true}
	r.FlowToken = ""
	_, err = f.a.receiveInboxIntake(r, binding.IssuedAt.Add(time.Second))
	require.ErrorContains(t, err, "quoted_reply_required")
	other := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	message := models.Message{BaseModel: models.BaseModel{ID: binding.ID}, OrganizationID: f.org.ID, ContactID: f.contact.ID, WhatsAppAccount: other.Name, WhatsAppMessageID: "wamid.synthetic_wrong_account", Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText}
	require.NoError(t, f.db.Create(&message).Error)
	r.ReplyToWAMID = message.WhatsAppMessageID
	_, err = f.a.receiveInboxIntake(r, binding.IssuedAt.Add(time.Second))
	require.ErrorContains(t, err, "response_mismatch")
	require.NoError(t, f.db.Model(&message).Update("whats_app_account", f.account.Name).Error)
	result, err := f.a.receiveInboxIntake(r, binding.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, "staged", result.State)
}

func TestInboxIntakeExpiryErasesStagingWithoutChangingRecordedValues(t *testing.T) {
	f := newIntakeBindingFixture(t)
	binding := f.bind(t)
	_, err := f.a.receiveInboxIntake(f.response(binding), binding.IssuedAt.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, expireInboxIntake(f.db, binding.ExpiresAt))
	result := f.stored(t, binding.ID)
	require.Equal(t, "expired", result.State)
	require.Empty(t, result.EncryptedAnswers)
	require.NoError(t, expireInboxIntake(f.db, binding.ExpiresAt.Add(time.Hour)))
	var count int64
	require.NoError(t, f.db.Model(&models.InboxFieldValue{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntakeOtherAccountTransferCannotSuppressThisAccount(t *testing.T) {
	f := newIntakeBindingFixture(t)
	other := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	transfer := models.AgentTransfer{OrganizationID: f.org.ID, ContactID: f.contact.ID, WhatsAppAccount: other.Name, PhoneNumber: f.contact.PhoneNumber, Status: models.TransferStatusActive, Source: models.TransferSourceFlow, TransferredAt: time.Now().UTC()}
	require.NoError(t, f.db.Create(&transfer).Error)
	require.True(t, f.a.hasActiveAgentTransfer(f.org.ID, f.contact.ID, other.Name))
	require.False(t, f.a.hasActiveAgentTransfer(f.org.ID, f.contact.ID, f.account.Name))
	require.True(t, f.a.hasActiveAgentTransfer(f.org.ID, f.contact.ID), "legacy aggregate callers retain their meaning")
}
