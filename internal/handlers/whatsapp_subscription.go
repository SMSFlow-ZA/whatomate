package handlers

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
)

func (a *App) whatsAppSubscriptionAccount(account *models.WhatsAppAccount) (*whatsapp.Account, error) {
	denied := errors.New("invalid account webhook subscription configuration")
	if a.Config == nil || account == nil {
		return nil, denied
	}
	result := a.toWhatsAppAccount(account)
	accounts, businesses := map[string]bool{}, map[string]bool{}
	for _, binding := range a.Config.WhatsApp.WebhookSubscriptionOverrides {
		org, orgErr := uuid.Parse(binding.OrganizationID)
		id, idErr := uuid.Parse(binding.AccountID)
		configured := whatsapp.WebhookSubscriptionOverride{CallbackURL: binding.CallbackURL, VerifyToken: "configuration-validation"}
		if orgErr != nil || idErr != nil || org == uuid.Nil || id == uuid.Nil || org.String() != binding.OrganizationID || id.String() != binding.AccountID ||
			binding.BusinessID == "" || strings.TrimSpace(binding.BusinessID) != binding.BusinessID || configured.Validate() != nil ||
			accounts[binding.AccountID] || businesses[binding.BusinessID] {
			return nil, denied
		}
		accounts[binding.AccountID], businesses[binding.BusinessID] = true, true
		// Either immutable account ID or WABA overlap requires the entire tuple
		// to match. Changed bindings must never fall back to the shared callback.
		if account.ID == id || account.BusinessID == binding.BusinessID {
			if account.ID != id || account.OrganizationID != org || account.BusinessID != binding.BusinessID {
				return nil, denied
			}
			override := &whatsapp.WebhookSubscriptionOverride{CallbackURL: binding.CallbackURL, VerifyToken: account.WebhookVerifyToken}
			if override.Validate() != nil {
				return nil, denied
			}
			result.SubscriptionOverride = override
		}
	}
	return result, nil
}
