package worker

import (
	"context"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestGenericCampaignRefusesProtectedOrganizationWithoutModuleConfig(t *testing.T) {
	w := testWorker(t)
	org := testutil.CreateTestOrganization(t, w.DB)
	protected := models.Contact{OrganizationID: org.ID, PhoneNumber: "+12025550199", ProtectionFields: models.ProtectionFields{ProtectionOrganizationID: org.ID.String(), ProtectionModule: "cases", ProtectionResource: "protected-case", ProtectionClassification: "sensitive"}}
	require.NoError(t, w.DB.Create(&protected).Error)
	// No campaign lookup, recipient mutation or provider call may precede this gate.
	e := w.HandleRecipientJob(context.Background(), &queue.RecipientJob{OrganizationID: org.ID})
	require.ErrorContains(t, e, "protected organization")
	require.False(t, models.GenericOrganizationAllowed(context.Background(), w.DB, org.ID))
}
