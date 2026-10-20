package handlers

import (
	"context"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/shridarpatil/whatomate/internal/models"
)

// StartExtensionWorker runs only the explicitly synthetic composition. There is
// deliberately no implicit carrier sender: durable intents are held after current
// eligibility checks until a reviewed transport adapter is configured.
func (a *App) StartExtensionWorker() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if a.Config == nil || !a.Config.Extensions.SyntheticDevelopment || a.ExtensionConversation == nil {
		return cancel
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick, done := context.WithTimeout(ctx, 15*time.Second)
				a.extensionSyntheticTick(tick)
				done()
			}
		}
	}()
	return cancel
}

func (a *App) extensionSyntheticTick(ctx context.Context) {
	db, e := a.DB.DB()
	if e != nil {
		return
	}
	var organizations []models.Organization
	if e = a.DB.WithContext(ctx).Select("id").Find(&organizations).Error; e != nil {
		return
	}
	for _, org := range organizations {
		worker := delivery.Worker{Tenant: org.ID.String(), DB: db, Authorize: a.ExtensionConversation.AuthorizeDelivery, Clock: time.Now, LeaseDuration: time.Minute}
		for range 100 {
			more, e := worker.Dispatch(ctx)
			if e != nil || !more {
				break
			}
		}
		repo := &cases.SQLRepository{DB: db}
		for range 100 {
			now := time.Now().UTC()
			claim, ok, e := repo.ClaimOutbox(ctx, org.ID.String(), "synthetic-ack-holder", now, now.Add(time.Minute))
			if e != nil || !ok {
				break
			}
			// Resolve and validate through the conversation owner's port. No generic
			// message/contact transcript is created, and no carrier success is invented.
			_, _ = a.ExtensionConversation.CaseAcknowledgement(ctx, claim.Message, "synthetic")
			if e = repo.FinishOutbox(ctx, claim, "held", ""); e != nil {
				break
			}
		}
	}
	_, _ = a.ExtensionConversation.EscalateDue(ctx)
}
