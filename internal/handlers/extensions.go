package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
)

func extensionIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// InitializeExtensions validates the actual compiled composition. No uploaded
// binary or config setting can register executable code. Registration of domain
// adapters is intentionally a separate reviewed build step.
func (a *App) InitializeExtensions() error {
	if a.Config == nil {
		return fmt.Errorf("missing extension configuration")
	}
	if a.Config.Extensions.ProtectedMode {
		return fmt.Errorf("protected mode is unavailable until host read/event/MFA integration is accepted")
	}
	if a.ExtensionRegistry == nil {
		a.ExtensionRegistry = ext.NewRegistry()
	}
	if a.ExtensionGuard == nil {
		a.ExtensionGuard = ext.NewGuard()
	}
	required := append(extensionIDs(a.Config.Extensions.Required), extensionIDs(a.Config.Extensions.RequiredInbound)...)
	return a.ExtensionRegistry.Validate(required)
}

func (a *App) checkExtensionInbound(ctx context.Context, org uuid.UUID, account string, msg IncomingTextMessage) error {
	if a.Config == nil {
		return ext.ErrDenied
	}
	required := extensionIDs(a.Config.Extensions.RequiredInbound)
	if len(required) == 0 {
		return nil
	}
	if a.ExtensionGuard == nil {
		return ext.ErrDenied
	}
	return a.ExtensionGuard.CheckInbound(ctx, ext.Inbound{OrganizationID: org.String(), AccountID: account, ProviderMessageID: msg.ID, Sender: msg.From, Kind: msg.Type}, required)
}

// Webhook dispatch has special branches which run before the text processor.
// Gate here as well, before any sender logging or special-branch persistence.
func (a *App) checkExtensionWebhookInbound(ctx context.Context, phoneID string, msg IncomingTextMessage) error {
	if a.Config == nil {
		return ext.ErrDenied
	}
	if len(extensionIDs(a.Config.Extensions.RequiredInbound)) == 0 {
		return nil
	}
	account, err := a.getWhatsAppAccountCached(phoneID)
	if err != nil {
		return ext.ErrDenied
	}
	return a.checkExtensionInbound(ctx, account.OrganizationID, account.ID.String(), msg)
}
