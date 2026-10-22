package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
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
	if a.Config.Database.SchemaManagedExternally {
		if a.DB == nil {
			return fmt.Errorf("external protected schema database unavailable")
		}
		db, err := a.DB.DB()
		if err != nil {
			return err
		}
		if err := protectedschema.Verify(context.Background(), db, a.Config); err != nil {
			return err
		}
		a.DB = plan.WithExternalSchema(a.DB)
	}
	if a.Config.Extensions.ProtectedMode {
		if err := validateProtectedExtensionSettings(a.Config.Extensions); err != nil {
			return err
		}
	}
	if a.DB != nil {
		protected, err := models.ProtectedHostEnabled(a.DB)
		if err != nil {
			return fmt.Errorf("protected host state unavailable: %w", err)
		}
		if protected && !a.Config.Extensions.ProtectedMode {
			return fmt.Errorf("protected database requires protected host configuration")
		}
	}
	if err := a.initializeExtensionSecurity(); err != nil {
		return err
	}
	if a.ExtensionRegistry == nil {
		a.ExtensionRegistry = ext.NewRegistry()
	}
	if a.ExtensionGuard == nil {
		a.ExtensionGuard = ext.NewGuard()
	}
	if err := a.initializeCompiledModules(); err != nil {
		return err
	}
	var carrierDB *sql.DB
	if a.DB != nil {
		var err error
		carrierDB, err = a.DB.DB()
		if err != nil {
			return err
		}
	}
	if err := a.initializeProtectedCarrier(context.Background(), carrierDB); err != nil {
		return err
	}
	if err := a.initializeExtensionVectors(context.Background(), carrierDB); err != nil {
		return fmt.Errorf("knowledge vector initialization failed: %w", err)
	}
	required := append(extensionIDs(a.Config.Extensions.Required), extensionIDs(a.Config.Extensions.RequiredInbound)...)
	required = append(required, extensionIDs(a.Config.Extensions.Modules)...)
	if err := a.ExtensionRegistry.Validate(required); err != nil {
		return err
	}
	if a.Config.Extensions.ProtectedMode {
		if a.DB == nil || a.ExtensionSecurity == nil || a.ExtensionAccess == nil || a.ExtensionConversation == nil {
			return fmt.Errorf("protected ER composition incomplete")
		}
		if !a.Config.Database.SchemaManagedExternally {
			if err := models.EnableProtectedHost(a.DB); err != nil {
				return fmt.Errorf("protected host activation failed: %w", err)
			}
		}
	}
	return nil
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
