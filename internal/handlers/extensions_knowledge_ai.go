package handlers

import (
	"context"
	"database/sql"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
)

func (a *App) initializeKnowledge(ctx context.Context, db *sql.DB) error {
	if e := knowledge.MigrateHTTP(ctx, db); e != nil {
		return e
	}
	a.ExtensionAPI["knowledge"] = knowledge.NewHTTPHandler(db, knowledge.HTTPConfig{Enabled: true, Authorize: a.authorizeExtensionKnowledge})
	return a.ExtensionRegistry.Register(ext.Descriptor{ID: "knowledge", Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security"}, Capabilities: []string{"quarantined-intake", "human-publication", "current-audience-retrieval"}})
}

func (a *App) initializeAI(ctx context.Context, db *sql.DB) error {
	c := a.Config.Extensions
	provider, e := newExtensionAIProvider(extensionAIProviderConfigFromHost(c))
	if e != nil {
		return e
	}
	if e = (ai.SQLStore{DB: db}).Migrate(ctx); e != nil {
		return e
	}
	cfg := ai.HTTPConfig{Enabled: true, Classifier: provider, Labels: []string{"Attendance concern", "Workplace concern", "Conflict of interest", "Escalation request"}}
	cfg.Authorize = func(ctx context.Context, original addonapi.Actor, permission, audience string) bool {
		current, e := a.refreshExtensionActor(ctx, original)
		return e == nil && current.Has(permission) && current.AudienceAllowed("knowledge:read", audience)
	}
	cfg.Cases = func(ctx context.Context, original addonapi.Actor, id, permission string) bool {
		current, e := a.refreshExtensionActor(ctx, original)
		if e != nil || permission != string(cases.WorkAssigned) {
			return false
		}
		p := cases.PrincipalForActor(current)
		c, e := (cases.Service{Repository: &cases.SQLRepository{DB: db}}).Read(ctx, p, id)
		return e == nil && cases.CanWork(p, c)
	}
	cfg.ServiceFor = func(ctx context.Context, original addonapi.Actor) (ai.Service, error) {
		current, e := a.refreshExtensionActor(ctx, original)
		if e != nil {
			return ai.Service{}, e
		}
		if e = knowledge.SynchronizeActor(ctx, db, knowledge.HTTPConfig{Enabled: true, Authorize: a.authorizeExtensionKnowledge}, current); e != nil {
			return ai.Service{}, e
		}
		repo, e := knowledge.NewPostgresRepository(db, current.OrganizationID)
		if e != nil {
			return ai.Service{}, e
		}
		auth := knowledge.AuthorizeFunc(func(ctx context.Context, user, audience string) bool {
			refreshed, e := a.refreshExtensionActor(ctx, original)
			return e == nil && user == refreshed.UserID && refreshed.AudienceAllowed("knowledge:read", audience)
		})
		vectors, e := a.extensionVectors(ctx, db, current.OrganizationID)
		if e != nil {
			return ai.Service{}, e
		}
		retriever := ai.HybridRetriever{Catalog: repo, Candidates: repo, Embeddings: provider}
		// A typed nil *VectorRepository is nonnil when assigned to an interface.
		if vectors != nil {
			retriever.Vectors = vectors
		}
		return ai.Service{Catalog: retriever, Authorizer: auth, Provider: provider, Now: time.Now}, nil
	}
	a.ExtensionAPI["ai"] = ai.NewHTTPHandler(db, cfg)
	return a.ExtensionRegistry.Register(ext.Descriptor{ID: "ai", Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security", "knowledge", "cases"}, Capabilities: []string{"configured-advisory-inference", "source-bound-quotes", "human-corrections"}})
}
