package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/reports"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
)

func (a *App) initializeReports(ctx context.Context, db *sql.DB) error {
	if a.Config.Extensions.ReportsMinGroup < 2 {
		return fmt.Errorf("explicit report disclosure policy required")
	}
	if e := a.extensionSchemaMigration(ctx, db, func() error { return (reports.Store{DB: db}).Migrate(ctx) }); e != nil {
		return e
	}
	a.ExtensionAPI["reports"] = reports.NewHTTPHandler(db, reports.HTTPConfig{Enabled: true, MinGroup: a.Config.Extensions.ReportsMinGroup, Clock: time.Now, RefreshActor: a.refreshExtensionActor, Activities: func(ctx context.Context, actor addonapi.Actor, board bool) ([]reports.ActivityFact, error) {
		facts, e := conversation.ActivityFactsFor(ctx, db, actor, board)
		if e != nil {
			return nil, e
		}
		result := make([]reports.ActivityFact, 0, len(facts))
		for _, f := range facts {
			result = append(result, reports.ActivityFact{ID: f.ID, OrganizationID: f.OrganizationID, UnitID: f.UnitID, CaseKey: f.CaseKey, Zone: f.Zone, Department: f.Department, CaseType: f.CaseType, Kind: f.Kind, Outcome: f.Outcome, OccurredAt: f.OccurredAt})
		}
		return result, nil
	}})
	return a.ExtensionRegistry.Register(ext.Descriptor{ID: "reports", Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security", "cases", "conversation"}, Capabilities: []string{"historical-case-projections", "activity-volumes", "controlled-exports", "leadership-suppression"}})
}

func (a *App) initializeConversation(ctx context.Context, db *sql.DB) error {
	c := a.Config.Extensions
	if err := validateExtensionConversationConfig(c); err != nil {
		return err
	}
	provider, e := newExtensionAIProvider(extensionAIProviderConfigFromHost(c))
	if e != nil {
		return e
	}
	if e = a.extensionSchemaMigration(ctx, db, func() error { return conversation.Migrate(ctx, db) }); e != nil {
		return e
	}
	if e = a.extensionSchemaMigration(ctx, db, func() error { return (&cases.SQLRepository{DB: db}).MigrateOutboxDelivery(ctx) }); e != nil {
		return e
	}
	cfg := conversation.HTTPConfig{EnabledSyntheticDevelopment: c.SyntheticDevelopment, Freshness: time.Duration(c.RosterFreshnessSeconds) * time.Second, Clock: time.Now, Aliases: a.ExtensionAccess.EmployeeAliases, EscalationAfter: time.Duration(c.EscalationAfterSeconds) * time.Second}
	if !c.SyntheticDevelopment && c.AIProcessingScope == "global_standard" {
		// The provider was validated above. Use a server-owned public notice,
		// never freeform operator configuration or approval identifiers.
		cfg.ProcessingDisclosure = &conversation.ProcessingDisclosure{Revision: "global-standard-v1", Text: extensionGlobalAIProcessingNotice}
	}
	cfg.LoadEscalationCase = func(ctx context.Context, org, unit, id string) (cases.Case, error) {
		_, record, e := a.extensionTimerWorker(ctx, db, org, unit, id)
		return record, e
	}
	cfg.ResolveCandidates = func(ctx context.Context, record cases.Case) (cases.Principal, []cases.Candidate, error) {
		actor, _, e := a.extensionTimerWorker(ctx, db, record.OrganizationID, record.UnitID, record.ID)
		if e != nil {
			return cases.Principal{}, nil, e
		}
		var result []cases.Candidate
		for i, ids := range []string{c.RoutingPrimaryIDs, c.RoutingBackupIDs} {
			for _, id := range extensionIDs(ids) {
				current := false
				for _, assignment := range record.Assignments {
					if assignment.ID == id {
						current = true
					}
				}
				if current {
					continue
				}
				member, e := a.extensionMember(ctx, record.OrganizationID, id)
				if e != nil {
					continue
				}
				result = append(result, cases.Candidate{Principal: cases.PrincipalForActor(member), Role: cases.Handler, Backup: i == 1})
			}
		}
		return actor, result, nil
	}
	cfg.Advisor = func(ctx context.Context, employee addonapi.Actor, question string) (conversation.Answer, error) {
		principal := "employee:" + employee.UserID
		auth := knowledge.AuthorizeFunc(func(ctx context.Context, id, audience string) bool {
			if id != principal || audience != c.ConversationEmployeeAudience {
				return false
			}
			store, e := directory.NewPostgresStore(db, employee.OrganizationID, "verified-reader")
			if e != nil {
				return false
			}
			snapshot, e := store.Load(ctx)
			if e != nil {
				return false
			}
			for _, v := range snapshot.Employees {
				if v.ID == employee.UserID {
					matched, e := directory.Match(snapshot, v.Phone, time.Now(), cfg.Freshness)
					return e == nil && matched.ID == employee.UserID
				}
			}
			return false
		})
		if e := knowledge.EnsureVerifiedReader(ctx, db, employee.OrganizationID, principal, c.ConversationEmployeeAudience, auth); e != nil {
			return conversation.Answer{}, e
		}
		repo, e := knowledge.NewPostgresRepository(db, employee.OrganizationID)
		if e != nil {
			return conversation.Answer{}, e
		}
		retriever := ai.HybridRetriever{Catalog: repo, Candidates: repo, Embeddings: provider, Fallback: ai.FallbackMode(c.AIRetrievalFallback)}
		vectors, e := a.extensionVectors(ctx, db, employee.OrganizationID)
		if e != nil {
			return conversation.Answer{}, e
		}
		if vectors != nil {
			retriever.Vectors = vectors
		}
		result := (ai.Service{Catalog: retriever, Provider: provider, Authorizer: auth, Now: time.Now}).Answer(ctx, principal, c.ConversationEmployeeAudience, question)
		return extensionConversationAnswer(result), nil
	}
	service := &conversation.Service{DB: db, Config: cfg}
	a.ExtensionConversation = service
	a.ExtensionAPI["conversation"] = conversation.NewHTTPHandler(db, cfg)
	return a.ExtensionRegistry.Register(ext.Descriptor{ID: "conversation", Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security", "directory", "cases", "knowledge", "ai"}, Capabilities: []string{"synthetic-channel-rehearsal", "confirmed-case-intake", "redacted-status", "current-employee-assistance"}})
}

func (a *App) extensionTimerWorker(ctx context.Context, db *sql.DB, org, unit, id string) (cases.Principal, cases.Case, error) {
	for _, worker := range extensionIDs(a.Config.Extensions.EscalationWorkerIDs) {
		member, e := a.extensionMember(ctx, org, worker)
		if e != nil {
			continue
		}
		principal := cases.PrincipalForActor(member)
		record, e := (cases.Service{Repository: &cases.SQLRepository{DB: db}}).Read(ctx, principal, id)
		if e == nil && record.OrganizationID == org && record.UnitID == unit && member.Allows(string(cases.AssignTriage), unit) {
			return principal, record, nil
		}
	}
	return cases.Principal{}, cases.Case{}, cases.ErrDenied
}
