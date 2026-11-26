package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errInboxRoutingName = errors.New("an active routing policy already uses this name")

// New routing interfaces recheck authority from PostgreSQL, rather than grant
// settings access from a stale role cache or request-supplied administrator flag.
func (a *App) inboxRoutingAuthority(r *fastglue.Request, db *gorm.DB, action string) (auditViewer, bool) {
	org, user, err := a.requireInboxAuth(r, models.ResourceTeams, action)
	if err != nil {
		return auditViewer{}, false
	}
	// requireInboxAuth has verified the original bound identity and selected
	// organisation, including its stamp; this second read checks current authority.
	viewer, err := loadAuditViewer(db, org, user, nil)
	if errors.Is(err, errAuditUnauthenticated) || errors.Is(err, errAuditForbidden) || err == nil && !viewer.has(models.ResourceTeams, action) {
		_ = r.SendErrorEnvelope(403, "Routing settings permission is required", nil, "routing_permission_required")
		return viewer, false
	}
	if err != nil {
		_ = a.inboxError(r, err)
		return viewer, false
	}
	return viewer, true
}

func (a *App) inboxRoutingError(r *fastglue.Request, err error) error {
	switch {
	case errors.Is(err, errInboxRoutingName):
		return r.SendErrorEnvelope(409, errInboxRoutingName.Error(), nil, "routing_name_conflict")
	case errors.Is(err, errInboxRoutingConfig):
		return r.SendErrorEnvelope(422, errInboxRoutingConfig.Error(), nil, "routing_configuration_invalid")
	case errors.Is(err, errInboxAssignmentRate):
		return r.SendErrorEnvelope(409, errInboxAssignmentRate.Error(), nil, "assignment_rate_limited")
	default:
		return a.inboxError(r, err)
	}
}

func (a *App) ListInboxRoutingPolicies(r *fastglue.Request) error {
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	v, ok := a.inboxRoutingAuthority(r, db, models.ActionRead)
	if !ok {
		return nil
	}
	rows := []models.InboxRoutingPolicy{}
	q := db.Where("organization_id=?", v.Org)
	if string(r.RequestCtx.QueryArgs().Peek("archived")) == "true" {
		q = q.Where("archived_at IS NOT NULL")
	} else {
		q = q.Where("archived_at IS NULL")
	}
	if err := q.Order("name,id").Limit(201).Find(&rows).Error; err != nil {
		return a.inboxRoutingError(r, err)
	}
	more := len(rows) > 200
	if more {
		rows = rows[:200]
	}
	return r.SendEnvelope(map[string]any{"policies": rows, "has_more": more, "can_write": v.has(models.ResourceTeams, models.ActionWrite)})
}

type inboxRoutingPolicyInput struct {
	Revision       int64                     `json:"revision"`
	Name           string                    `json:"name"`
	Description    string                    `json:"description"`
	Strategy       models.AssignmentStrategy `json:"strategy"`
	MaxActive      *int                      `json:"max_active"`
	MaxAssignments int                       `json:"max_assignments"`
	WindowSeconds  int                       `json:"window_seconds"`
	Archive        bool                      `json:"archive"`
}

func (a *App) SaveInboxRoutingPolicy(r *fastglue.Request) error {
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	v, ok := a.inboxRoutingAuthority(r, db, models.ActionWrite)
	if !ok {
		return nil
	}
	var input inboxRoutingPolicyInput
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	id := uuid.Nil
	if raw, _ := r.RequestCtx.UserValue("policy_id").(string); raw != "" {
		var err error
		id, err = uuid.Parse(raw)
		if err != nil || id == uuid.Nil || input.Revision < 1 {
			return a.inboxRoutingError(r, errInboxTransition)
		}
	} else if input.Archive {
		return a.inboxRoutingError(r, errInboxTransition)
	}
	var policy, previous models.InboxRoutingPolicy
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-routing-policies:"+v.Org.String()).Error; err != nil {
			return err
		}
		if id != uuid.Nil {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&policy, "id=? AND organization_id=?", id, v.Org).Error; err != nil {
				return err
			}
			if policy.Revision != input.Revision || policy.ArchivedAt != nil {
				return errInboxConflict
			}
			previous = policy
			policy.Revision++
		} else {
			var count int64
			if err := tx.Model(&models.InboxRoutingPolicy{}).Where("organization_id=?", v.Org).Count(&count).Error; err != nil {
				return err
			}
			if count >= 200 {
				return errInboxTransition
			}
			policy = models.InboxRoutingPolicy{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: v.Org, Revision: 1, CreatedByID: v.User}
		}
		policy.UpdatedByID = v.User
		if input.Archive {
			now := time.Now().UTC()
			policy.ArchivedAt = &now
		} else {
			policy.Name, policy.Description, policy.Strategy = input.Name, input.Description, input.Strategy
			policy.MaxActive, policy.MaxAssignments, policy.WindowSeconds = input.MaxActive, input.MaxAssignments, input.WindowSeconds
			if policy.Validate() != nil {
				return errInboxTransition
			}
			var duplicates int64
			if err := tx.Model(&models.InboxRoutingPolicy{}).Where("organization_id=? AND id<>? AND archived_at IS NULL AND lower(name)=lower(?)", v.Org, policy.ID, policy.Name).Count(&duplicates).Error; err != nil {
				return err
			}
			if duplicates != 0 {
				return errInboxRoutingName
			}
		}
		return tx.Save(&policy).Error
	})
	if err != nil {
		return a.inboxRoutingError(r, err)
	}
	action := models.AuditActionCreated
	if id != uuid.Nil {
		action = models.AuditActionUpdated
	}
	a.logAudit(v.Org, v.User, "inbox_routing_policy", policy.ID, action, previous, policy)
	return r.SendEnvelope(map[string]any{"policy": policy})
}

func inboxRoutingScope(db *gorm.DB, org, accountID, teamID uuid.UUID) (models.WhatsAppAccount, models.Team, error) {
	var account models.WhatsAppAccount
	var team models.Team
	if accountID == uuid.Nil || teamID == uuid.Nil {
		return account, team, errInboxTransition
	}
	if err := db.Select("id,organization_id,name").First(&account, "id=? AND organization_id=?", accountID, org).Error; err != nil {
		return account, team, err
	}
	err := db.Select("id,organization_id,name,assignment_strategy,updated_at").First(&team, "id=? AND organization_id=? AND is_active", teamID, org).Error
	return account, team, err
}

func readInboxRoutingConfiguration(db *gorm.DB, org, account, team uuid.UUID) (*models.InboxRoutingConfiguration, error) {
	var config models.InboxRoutingConfiguration
	err := db.First(&config, "organization_id=? AND account_id=? AND team_id=?", org, account, team).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !models.ValidInboxRoutingStrategy(config.Strategy) || config.Revision < 1 || config.PolicyRevision < 1 {
		return nil, errInboxRoutingConfig
	}
	return &config, nil
}

func (a *App) GetInboxRoutingConfiguration(r *fastglue.Request) error {
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	v, ok := a.inboxRoutingAuthority(r, db, models.ActionRead)
	if !ok {
		return nil
	}
	accountID, err := parsePathUUID(r, "account_id", "WhatsApp account")
	if err != nil {
		return nil
	}
	teamID, err := parsePathUUID(r, "team_id", "Team")
	if err != nil {
		return nil
	}
	account, team, err := inboxRoutingScope(db, v.Org, accountID, teamID)
	if err != nil {
		return a.inboxRoutingError(r, err)
	}
	config, err := readInboxRoutingConfiguration(db, v.Org, accountID, teamID)
	if err != nil {
		return a.inboxRoutingError(r, err)
	}
	strategy := team.AssignmentStrategy
	if config != nil && config.Strategy != models.InboxRoutingInherit {
		strategy = config.Strategy
	}
	if !models.ValidInboxRoutingStrategy(strategy) || strategy == models.InboxRoutingInherit {
		return a.inboxRoutingError(r, errInboxRoutingConfig)
	}
	return r.SendEnvelope(map[string]any{"configuration": config, "account_name": account.Name, "team_name": team.Name, "team_strategy": team.AssignmentStrategy, "effective_strategy": strategy, "can_write": v.has(models.ResourceTeams, models.ActionWrite)})
}
