// Seed creates identities and exact grants only in a new, isolated synthetic DB.
// It never enrolls MFA, approves a roster, publishes documents or creates cases.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"os"
	"strings"
)

type login struct{ Role, Email, Password, UserID, EmployeeID string }
type result struct {
	Synthetic      bool
	OrganizationID string
	Logins         []login
}

func random() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func run() error {
	if os.Getenv("SYNTHETIC_LOCAL_ONLY") != "true" {
		return errors.New("synthetic flag required")
	}
	cfg, e := config.Load("/run/config/host.toml")
	if e != nil {
		return e
	}
	if cfg.App.Environment != "development" || !cfg.Extensions.SyntheticDevelopment || cfg.Database.Name != "whatomate_synthetic_local" {
		return errors.New("dedicated local DB required")
	}
	db, e := database.NewPostgres(&cfg.Database, false)
	if e != nil {
		return e
	}
	sqlDB, e := db.DB()
	if e != nil {
		return e
	}
	defer sqlDB.Close()
	if os.Getenv("SYNTHETIC_SYNC_VISIBILITY") == "true" {
		return syncVisibility(db)
	}
	if e = database.AutoMigrate(db); e != nil {
		return e
	}
	var n int64
	if e = db.Model(&models.Organization{}).Count(&n).Error; e != nil {
		return e
	}
	if n != 0 {
		return errors.New("seed refuses existing organisation; passwords are never reset")
	}
	org := models.Organization{BaseModel: models.BaseModel{ID: uuid.New()}, Name: "SYNTHETIC ER demonstration", Slug: "synthetic-er-local"}
	out := result{Synthetic: true, OrganizationID: org.ID.String()}
	common := []string{"security:mfa.enroll", "security:mfa.revoke"}
	roles := map[string][]string{
		"employee":   {"cases:read", "cases:submit.own", "cases:read.own", "knowledge:read", "ai:assist"},
		"manager":    {"cases:read", "cases:submit.own", "cases:read.own", "knowledge:read", "ai:assist", "conversation:simulate"},
		"er":         {"cases:read", "cases:read.triage", "cases:assign.triage", "cases:work.assigned", "knowledge:read", "ai:assist", "ai:review", "reports:operational", "conversation:simulate"},
		"hrbp":       {"cases:read", "cases:read.triage", "cases:assign.triage", "cases:work.assigned", "knowledge:read", "ai:assist", "ai:review", "reports:operational"},
		"leadership": {"reports:aggregate"}, "unauthorized": {},
		"administrator": {"directory:read", "directory:import", "directory:approve", "directory:revoke", "knowledge:read", "knowledge:upload", "knowledge:review", "conversation:simulate", "governance:inventory.write", "governance:hold.manage", "governance:journal.read", "governance:restore.reconcile"},
		"publisher":     {"knowledge:read", "knowledge:review", "knowledge:publish", "knowledge:retire"},
	}
	for name := range roles {
		for _, grant := range supplementalGrants(name) {
			found := false
			for _, permission := range roles[name] {
				if permission == grant.Permission {
					found = true
				}
			}
			if !found {
				roles[name] = append(roles[name], grant.Permission)
			}
		}
	}
	e = db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&org).Error; e != nil {
			return e
		}
		for _, name := range []string{"employee", "manager", "er", "hrbp", "leadership", "unauthorized", "administrator", "publisher"} {
			permissions := append(append([]string{}, common...), roles[name]...)
			role := models.CustomRole{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Synthetic " + name}
			for _, p := range permissions {
				pair := strings.SplitN(p, ":", 2)
				perm := models.Permission{Resource: pair[0], Action: pair[1]}
				if e := tx.Where("resource=? AND action=?", perm.Resource, perm.Action).FirstOrCreate(&perm).Error; e != nil {
					return e
				}
				role.Permissions = append(role.Permissions, perm)
			}
			if e := tx.Create(&role).Error; e != nil {
				return e
			}
			password := random()
			hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if e != nil {
				return e
			}
			u := models.User{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Email: name + "@synthetic.invalid", FullName: "Synthetic " + name, PasswordHash: string(hash), RoleID: &role.ID, IsActive: true, AuthStamp: uuid.NewString()}
			if e = tx.Create(&u).Error; e != nil {
				return e
			}
			membership := models.UserOrganization{UserID: u.ID, OrganizationID: org.ID, RoleID: &role.ID, IsDefault: true}
			if e = tx.Create(&membership).Error; e != nil {
				return e
			}
			out.Logins = append(out.Logins, login{Role: name, Email: u.Email, Password: password, UserID: u.ID.String(), EmployeeID: "DEMO-" + strings.ToUpper(name)})
		}
		return nil
	})
	if e != nil {
		return e
	}
	access := api.AccessStore{DB: sqlDB}
	ctx := context.Background()
	if e = access.Migrate(ctx); e != nil {
		return e
	}
	for _, u := range out.Logins {
		var grants []api.Grant
		for _, p := range roles[u.Role] {
			g := api.Grant{Permission: p, UnitID: "demo-er"}
			if strings.HasPrefix(p, "governance:") && (u.Role == "administrator" || u.Role == "publisher") {
				g.UnitID = "employees"
			}
			if strings.HasPrefix(p, "knowledge:") || strings.HasPrefix(p, "ai:") {
				g = api.Grant{Permission: p, Audience: "employees"}
			}
			grants = append(grants, g)
		}
		for _, g := range supplementalGrants(u.Role) {
			found := false
			for _, existing := range grants {
				if existing == g {
					found = true
				}
			}
			if !found {
				grants = append(grants, g)
			}
		}
		if e = access.Replace(ctx, out.OrganizationID, u.UserID, grants); e != nil {
			return e
		}
		if e = access.LinkIdentity(ctx, out.OrganizationID, u.UserID, u.EmployeeID); e != nil {
			return e
		}
	}
	path := "/run/private/identities.json"
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(out)
}

// syncVisibility updates only the explicit synthetic case-visibility and content
// reviewer grants. It never resets passwords, factor enrollment or generations.
func syncVisibility(db *gorm.DB) error {
	b, e := os.ReadFile("/run/private/identities.json")
	if e != nil {
		return e
	}
	var identities result
	if json.Unmarshal(b, &identities) != nil || !identities.Synthetic {
		return errors.New("synthetic identity marker required")
	}
	var org models.Organization
	if e = db.Where("id=? AND slug=?", identities.OrganizationID, "synthetic-er-local").First(&org).Error; e != nil {
		return e
	}
	raw, e := db.DB()
	if e != nil {
		return e
	}
	store := api.AccessStore{DB: raw}
	for _, item := range identities.Logins {
		additions := supplementalGrants(item.Role)
		if len(additions) == 0 {
			continue
		}
		var user models.User
		if e = db.Preload("Role.Permissions").Where("id=? AND email=? AND organization_id=?", item.UserID, item.Email, org.ID).First(&user).Error; e != nil {
			return e
		}
		if user.Role == nil || user.Role.Name != "Synthetic "+item.Role {
			return errors.New("unexpected synthetic role")
		}
		for _, grant := range additions {
			pair := strings.SplitN(grant.Permission, ":", 2)
			permission := models.Permission{Resource: pair[0], Action: pair[1]}
			if e = db.Where("resource=? AND action=?", permission.Resource, permission.Action).FirstOrCreate(&permission).Error; e != nil {
				return e
			}
			if e = db.Model(user.Role).Association("Permissions").Append(&permission); e != nil {
				return e
			}
			actor := api.Actor{OrganizationID: org.ID.String(), UserID: user.ID.String()}
			for _, p := range user.Role.Permissions {
				actor.Permissions = append(actor.Permissions, p.Resource+":"+p.Action)
			}
			actor.Permissions = append(actor.Permissions, grant.Permission)
			loaded, e := store.Load(context.Background(), actor)
			if e != nil {
				return e
			}
			if item.Role == "administrator" || item.Role == "publisher" {
				filtered := loaded.Grants[:0]
				for _, existing := range loaded.Grants {
					if strings.HasPrefix(existing.Permission, "governance:") && existing.UnitID != "employees" {
						continue
					}
					filtered = append(filtered, existing)
				}
				loaded.Grants = filtered
			}
			exists := false
			for _, g := range loaded.Grants {
				if g == grant {
					exists = true
				}
			}
			if !exists {
				loaded.Grants = append(loaded.Grants, grant)
			}
			if e = store.Replace(context.Background(), actor.OrganizationID, actor.UserID, loaded.Grants); e != nil {
				return e
			}
		}
	}
	// Earlier local migration startup may have created the upstream default
	// account. Disable that exact synthetic-db-only account; never reset users.
	return db.Model(&models.User{}).Where("email=? AND organization_id=? AND is_active=true", "admin@admin.com", org.ID).Updates(map[string]any{"is_active": false, "auth_stamp": uuid.NewString()}).Error
}

// supplementalGrants names the exact permissions for the disposable acceptance
// exercise. It is not a production provisioning policy or public admin endpoint.
func supplementalGrants(role string) []api.Grant {
	var grants []api.Grant
	unit := "demo-er"
	switch role {
	case "leadership":
		return []api.Grant{{Permission: "reports:export", UnitID: unit}}
	case "employee", "manager":
		return []api.Grant{{Permission: "cases:read", UnitID: unit}}
	case "er", "hrbp":
		grants = append(grants, api.Grant{Permission: "reports:export", UnitID: unit})
		grants = append(grants, api.Grant{Permission: "cases:read", UnitID: unit}, api.Grant{Permission: "directory:privacy", UnitID: unit}, api.Grant{Permission: "directory:read", UnitID: unit}, api.Grant{Permission: "conversation:privacy", UnitID: unit})
	case "administrator":
		unit = "employees"
		grants = append(grants, api.Grant{Permission: "knowledge:review", Audience: "employees"})
	case "publisher":
		unit = "employees"
	default:
		return nil
	}
	for _, action := range []string{"policy.manage", "request.access", "request.correct", "request.export", "request.delete", "deletion.approve", "deletion.record", "inventory.write", "hold.manage", "restore.reconcile", "journal.read"} {
		grants = append(grants, api.Grant{Permission: "governance:" + action, UnitID: unit})
	}
	grants = append(grants, api.Grant{Permission: "knowledge:read", Audience: "employees"}, api.Grant{Permission: "knowledge:retire", Audience: "employees"})
	return grants
}
func main() {
	if e := run(); e != nil {
		os.Stderr.WriteString("synthetic seed failed: " + e.Error() + "\n")
		os.Exit(1)
	}
}
