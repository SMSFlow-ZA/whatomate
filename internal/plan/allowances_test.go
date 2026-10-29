package plan

import (
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestDisabledAllowancesRetainOrdinaryMultiOrganizationDefaults(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, Install(db, config.PlanConfig{}))
	var exists bool
	require.NoError(t, db.Raw("SELECT to_regclass('whatomate_allowance_state') IS NOT NULL").Scan(&exists).Error)
	require.False(t, exists)
	for range 3 {
		require.NoError(t, db.Exec("INSERT INTO organizations(name) VALUES ('Example')").Error)
	}
}
func TestEnabledAllowancesKeepMultipleOrganizationsUnlessExplicitlyLimited(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(3, 3)
	require.NoError(t, Install(db, cfg))
	for range 3 {
		require.NoError(t, db.Exec("INSERT INTO organizations(name) VALUES ('Example')").Error)
	}
	cfg.MaxOrganizations = 1
	require.NoError(t, Install(db, cfg))
	err := db.Exec("INSERT INTO organizations(name) VALUES ('Additional')").Error
	resource, ok := Classify(err)
	require.True(t, ok)
	require.Equal(t, "organizations", resource)
	require.NoError(t, db.Exec("UPDATE organizations SET name='Preserved'").Error)
	require.NoError(t, db.Exec("DELETE FROM organizations").Error)
	require.NoError(t, db.Exec("INSERT INTO organizations(name) VALUES ('One')").Error)
	require.Error(t, Install(db, config.PlanConfig{}), "restore/restart cannot silently remove installed enforcement")
}
func TestLowerAllowancesPreserveDataAndAllowReductions(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(3, 3)
	require.NoError(t, Install(db, cfg))
	for i := range 3 {
		require.NoError(t, db.Exec("INSERT INTO users(email,is_super_admin) VALUES (?,true)", fmt.Sprintf("user%d@example.invalid", i)).Error)
	}
	cfg.MaxUsers = 1
	require.NoError(t, Install(db, cfg))
	require.NoError(t, db.Exec("UPDATE users SET full_name='Preserved'").Error)
	var count int64
	require.NoError(t, db.Table("users").Count(&count).Error)
	require.EqualValues(t, 3, count)
	err := db.Exec("INSERT INTO users(email) VALUES ('blocked@example.invalid')").Error
	resource, ok := Classify(err)
	require.True(t, ok)
	require.Equal(t, "users", resource)
	require.NoError(t, db.Exec("UPDATE users SET deleted_at=now()").Error)
	require.Error(t, db.Exec("UPDATE users SET deleted_at=NULL").Error, "multi-row restore must preserve the allowance")
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('new@example.invalid')").Error)
	_, usage, err := Snapshot(db)
	require.NoError(t, err)
	require.EqualValues(t, 1, usage.Users)
}
func TestConcurrentEntityAdditionsCannotExceedAllowances(t *testing.T) {
	for _, resource := range []string{"users", "numbers", "organizations"} {
		t.Run(resource, func(t *testing.T) {
			db := quotaDB(t)
			cfg := quotaConfig(1, 1)
			cfg.MaxOrganizations = 1
			require.NoError(t, Install(db, cfg))
			var group sync.WaitGroup
			results := make(chan error, 16)
			for i := range 16 {
				group.Add(1)
				go func(i int) {
					defer group.Done()
					var err error
					switch resource {
					case "users":
						err = db.Exec("INSERT INTO users(email,is_super_admin) VALUES (?,true)", fmt.Sprintf("u%d@example.invalid", i)).Error
					case "numbers":
						err = db.Exec("INSERT INTO whatsapp_accounts(name) VALUES ('Number')").Error
					default:
						err = db.Exec("INSERT INTO organizations(name) VALUES ('Organization')").Error
					}
					results <- err
				}(i)
			}
			group.Wait()
			close(results)
			accepted := 0
			for err := range results {
				if err == nil {
					accepted++
				} else {
					got, ok := Classify(err)
					require.True(t, ok, err)
					require.Equal(t, resource, got)
				}
			}
			require.Equal(t, 1, accepted)
		})
	}
}

func TestSnapshotUsesInstalledLimitsAndCountsPreservedRows(t *testing.T) {
	db := quotaDB(t)
	cfg := quotaConfig(4, 5)
	require.NoError(t, Install(db, cfg))
	require.NoError(t, db.Exec("INSERT INTO users(email) VALUES ('example@example.invalid'); INSERT INTO whatsapp_accounts(name) VALUES ('Number'); INSERT INTO organizations(name) VALUES ('One'),('Two')").Error)
	cfg.MaxUsers = 2
	cfg.MaxNumbers = 3
	cfg.MaxOrganizations = 1
	require.NoError(t, Install(db, cfg))
	current, usage, err := Snapshot(db)
	require.NoError(t, err)
	require.Equal(t, 2, current.MaxUsers)
	require.Equal(t, 3, current.MaxNumbers)
	require.Equal(t, 1, current.MaxOrganizations)
	require.EqualValues(t, 1, usage.Users)
	require.EqualValues(t, 1, usage.Numbers)
	require.EqualValues(t, 2, usage.Organizations)
}
func TestClassifyOnlyStructuredEntityAllowanceConstraints(t *testing.T) {
	for _, resource := range []string{"users", "numbers", "organizations"} {
		got, ok := Classify(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23514", ConstraintName: "whatomate_plan_" + resource + "_limit"}))
		require.True(t, ok)
		require.Equal(t, resource, got)
	}
	for _, err := range []error{nil, errors.New("whatomate_plan_users_limit"), &pgconn.PgError{Code: "23505", ConstraintName: "whatomate_plan_users_limit"}, &pgconn.PgError{Code: "23514", ConstraintName: "unrelated_constraint"}} {
		_, ok := Classify(err)
		require.False(t, ok)
	}
}
