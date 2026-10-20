package handlers

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/zerodha/logf"
)

func TestWebhookEligibilityPrecedesLogsAndSpecialMessageBranches(t *testing.T) {
	a := webhookTestApp(t)
	a.Config.Extensions = config.ExtensionsConfig{RequiredInbound: "directory"}
	var output bytes.Buffer
	a.Log = logf.New(logf.Opts{Writer: &output})
	org := testutil.CreateTestOrganization(t, a.DB)
	acc := models.WhatsAppAccount{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "gate-" + uuid.NewString(), PhoneID: uuid.NewString(), BusinessID: "synthetic", AccessToken: "synthetic"}
	if err := a.DB.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	sender := "+27123456789"
	body := fmt.Sprintf(`{"entry":[{"changes":[{"field":"messages","value":{"metadata":{"phone_number_id":%q},"messages":[{"id":"synthetic","from":%q,"type":"interactive","interactive":{"type":"call_permission_reply","call_permission_reply":{"expiration_timestamp":"9223372036854775808"}}}]}}]}]}`, acc.PhoneID, sender)
	r := testutil.NewRequest(t)
	r.RequestCtx.Request.SetBodyString(body)
	if err := a.WebhookHandler(r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), sender) {
		t.Fatalf("rejected sender logged before eligibility: %s", output.String())
	}
	if strings.Contains(output.String(), "expiration timestamp") {
		t.Fatal("special message branch bypassed eligibility gate")
	}
	if !strings.Contains(output.String(), "rejected by extension policy") {
		t.Fatalf("eligibility gate did not handle message: %s", output.String())
	}
}
