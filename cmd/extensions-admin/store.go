package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
)

type Result struct {
	Applied        bool               `json:"applied"`
	OrganizationID string             `json:"organization_id"`
	UserID         string             `json:"user_id"`
	CurrentRoleID  string             `json:"current_role_id"`
	Access         api.AccessSnapshot `json:"access"`
	Permissions    []string           `json:"permissions"`
	ProposedGrants []api.Grant        `json:"proposed_grants,omitempty"`
	PolicySHA256   string             `json:"policy_sha256,omitempty"`
	PlanSHA256     string             `json:"plan_sha256,omitempty"`
}

func checkRuntimeBinding(cfg *config.Config, p Profile, user string) error {
	if cfg == nil {
		return errors.New("host configuration required")
	}
	if p.Kind == "routing" && !contains(strings.Split(cfg.Extensions.RoutingPrimaryIDs, ","), user) && !contains(strings.Split(cfg.Extensions.RoutingBackupIDs, ","), user) {
		return errors.New("routing worker must be explicitly bound in host configuration")
	}
	if p.Kind == "escalation" && !contains(strings.Split(cfg.Extensions.EscalationWorkerIDs, ","), user) {
		return errors.New("escalation worker must be explicitly bound in host configuration")
	}
	return nil
}

// memberRoles locks current users only on apply. Primary-organization
// membership is required even for superadmins; no admin/superadmin bypass.
func memberRoles(ctx context.Context, tx *sql.Tx, policy Policy, target string, apply bool) (string, error) {
	var org string
	q := `SELECT id::text FROM organizations WHERE id=$1 AND deleted_at IS NULL`
	if apply {
		q += ` FOR SHARE`
	}
	if err := tx.QueryRowContext(ctx, q, policy.OrganizationID).Scan(&org); err != nil {
		return "", errors.New("configured organization unavailable")
	}
	q = `SELECT id::text,role_id::text FROM users WHERE organization_id=$1 AND id IN ($2,$3) AND is_active=true AND deleted_at IS NULL ORDER BY id`
	if apply {
		q += ` FOR UPDATE`
	}
	rows, err := tx.QueryContext(ctx, q, org, policy.OperatorID, target)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	seen := map[string]bool{}
	role := ""
	for rows.Next() {
		var id string
		var roleID sql.NullString
		if err := rows.Scan(&id, &roleID); err != nil {
			return "", err
		}
		seen[id] = true
		if id == target {
			role = roleID.String
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if !seen[policy.OperatorID] || !seen[target] {
		return "", errors.New("operator and target must be current active members of the configured organization")
	}
	return role, nil
}

func inspect(ctx context.Context, db *sql.DB, policy Policy, target string) (Result, error) {
	if err := validatePolicy(policy); err != nil {
		return Result{}, err
	}
	if !exactID(target) || !allowedTarget(policy, target) {
		return Result{}, errors.New("target outside private policy")
	}
	out := Result{OrganizationID: policy.OrganizationID, UserID: target}
	var err error
	out.Access, err = (api.AccessStore{DB: db}).InspectVersioned(ctx, policy.OrganizationID, target, func(ctx context.Context, tx *sql.Tx, _ bool) error {
		role, e := memberRoles(ctx, tx, policy, target, false)
		if e != nil {
			return e
		}
		out.CurrentRoleID = role
		out.Permissions = []string{}
		if role == "" {
			return nil
		}
		rows, e := tx.QueryContext(ctx, `SELECT p.resource||':'||p.action FROM custom_roles r JOIN role_permissions rp ON rp.custom_role_id=r.id JOIN permissions p ON p.id=rp.permission_id WHERE r.id=$1 AND r.organization_id=$2 AND r.deleted_at IS NULL AND p.deleted_at IS NULL ORDER BY p.resource,p.action`, role, policy.OrganizationID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var p string
			if e := rows.Scan(&p); e != nil {
				return e
			}
			out.Permissions = append(out.Permissions, p)
		}
		return rows.Err()
	})
	return out, err
}

func checkHumanIdentity(ctx context.Context, tx *sql.Tx, store api.AccessStore, cfg *config.Config, policy Policy, profile Profile, user string, apply bool) error {
	if profile.Kind != "operator" {
		return nil
	}
	if profile.SubjectKind == "nonemployee" {
		links, err := store.IdentityLinksTx(ctx, tx, policy.OrganizationID, user)
		if err != nil {
			return err
		}
		if policy.NonemployeeOperators[user] == "" || len(links) != 0 {
			return errors.New("attested nonemployee operator must not claim employee identity links")
		}
		return nil
	}
	binding, ok := policy.EmployeeBindings[user]
	if !ok || cfg == nil || cfg.Extensions.RosterFreshnessSeconds <= 0 || int64(cfg.Extensions.RosterFreshnessSeconds) > int64((1<<63-1)/time.Second) {
		return errors.New("employee binding and valid host roster freshness are required")
	}
	if err := directory.ValidateIdentityTx(ctx, tx, policy.OrganizationID, binding, time.Duration(cfg.Extensions.RosterFreshnessSeconds)*time.Second, time.Now().UTC()); err != nil {
		return errors.New("current approved employee identity/phone/roster proof denied")
	}
	return store.LinkIdentityTx(ctx, tx, policy.OrganizationID, user, binding.EmployeeID, apply)
}

func executePlan(ctx context.Context, db *sql.DB, cfg *config.Config, policy Policy, plan Plan, apply bool, policyHash, planHash string) (Result, error) {
	profile, err := validatePlan(policy, plan)
	if err != nil {
		return Result{}, err
	}
	if plan.Action == "replace" {
		if err := checkRuntimeBinding(cfg, profile, plan.UserID); err != nil {
			return Result{}, err
		}
	}
	roleID := uuid.NewString()
	proof := map[string]any{"profile": plan.Profile, "kind": profile.Kind, "subject_kind": profile.SubjectKind, "nonemployee_attestation": policy.NonemployeeOperators[plan.UserID], "previous_role_id": *plan.ExpectedRoleID, "new_role_id": roleID, "permissions": profile.Permissions, "policy_sha256": policyHash, "plan_sha256": planHash}
	if profile.Kind == "operator" && profile.SubjectKind == "employee" {
		binding := policy.EmployeeBindings[plan.UserID]
		identity, _ := json.Marshal([]string{policy.OrganizationID, plan.UserID, binding.EmployeeID})
		digest := sha256.Sum256(identity)
		proof["employee_identity_sha256"] = hex.EncodeToString(digest[:])
		proof["approved_roster_version"] = binding.ExpectedRosterVersion
	}
	metadata, _ := json.Marshal(proof)
	change := api.AccessReplacement{OrganizationID: policy.OrganizationID, UserID: plan.UserID, OperatorID: policy.OperatorID, Action: plan.Action, Reason: plan.Reason, ExpectedVersion: *plan.ExpectedVersion, Grants: profile.Grants, Metadata: metadata}
	out := Result{Applied: apply, OrganizationID: policy.OrganizationID, UserID: plan.UserID, CurrentRoleID: *plan.ExpectedRoleID, Permissions: append([]string{}, profile.Permissions...), ProposedGrants: append([]api.Grant{}, profile.Grants...), PolicySHA256: policyHash, PlanSHA256: planHash}
	store := api.AccessStore{DB: db}
	out.Access, err = store.ReplaceVersioned(ctx, change, apply, func(ctx context.Context, tx *sql.Tx, write bool) error {
		oldRole, e := memberRoles(ctx, tx, policy, plan.UserID, write)
		if e != nil {
			return e
		}
		if oldRole != *plan.ExpectedRoleID {
			return errors.New("host role changed; inspect and review a new plan")
		}
		if plan.Action == "replace" {
			if e := checkHumanIdentity(ctx, tx, store, cfg, policy, profile, plan.UserID, write); e != nil {
				return e
			}
		}
		if !write {
			return nil
		}
		// Create a new role for exactly this user. Existing/shared roles and
		// their permissions remain untouched; replacement never merges them.
		name := "ER/" + plan.UserID[:8] + "/" + roleID
		if _, e := tx.ExecContext(ctx, `INSERT INTO custom_roles(id,organization_id,name,description,is_system,is_default,created_at,updated_at) VALUES($1,$2,$3,$4,false,false,clock_timestamp(),clock_timestamp())`, roleID, policy.OrganizationID, name, "Explicit ER profile: "+plan.Profile); e != nil {
			return e
		}
		for _, permission := range profile.Permissions {
			pair := strings.SplitN(permission, ":", 2)
			id := uuid.NewString()
			if _, e := tx.ExecContext(ctx, `INSERT INTO permissions(id,resource,action,description,created_at,updated_at) VALUES($1,$2,$3,'Explicit ER permission',clock_timestamp(),clock_timestamp()) ON CONFLICT(resource,action) DO NOTHING`, id, pair[0], pair[1]); e != nil {
				return e
			}
			if e := tx.QueryRowContext(ctx, `SELECT id::text FROM permissions WHERE resource=$1 AND action=$2 AND deleted_at IS NULL`, pair[0], pair[1]).Scan(&id); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, `INSERT INTO role_permissions(custom_role_id,permission_id) VALUES($1,$2)`, roleID, id); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, `UPDATE users SET role_id=$1,updated_at=clock_timestamp() WHERE id=$2 AND organization_id=$3`, roleID, plan.UserID, policy.OrganizationID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE user_organizations SET role_id=$1,updated_at=clock_timestamp() WHERE user_id=$2 AND organization_id=$3 AND deleted_at IS NULL`, roleID, plan.UserID, policy.OrganizationID); e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if apply {
		out.CurrentRoleID = roleID
	}
	return out, nil
}
