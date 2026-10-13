package plan

import (
	"database/sql"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestManagedOrganizationRejectsStaleRepeatableReadSnapshot(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, Install(db, quotaConfig(2, 1)))
	tx := db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	require.NoError(t, tx.Error)
	t.Cleanup(func() { tx.Rollback() })
	var count int64
	require.NoError(t, tx.Table("organizations").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Exec("INSERT INTO organizations(name) VALUES ('winner')").Error)
	// A stronger isolation snapshot must abort rather than admit a second org.
	require.Error(t, tx.Exec("INSERT INTO organizations(name) VALUES ('stale')").Error)
	require.NoError(t, db.Table("organizations").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestUnmanagedOrganizationCreationPreserved(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, Install(db, quotaConfig(2, 1)))
	// Simulate upgrading the previous managed schema, which had no org counter.
	require.NoError(t, db.Exec("ALTER TABLE whatomate_plan_state DROP COLUMN used_organizations").Error)
	require.NoError(t, Install(db, quotaConfig(2, 1)))
	require.NoError(t, db.Exec("INSERT INTO organizations(name) VALUES ('one')").Error)
	require.Error(t, db.Exec("INSERT INTO organizations(name) VALUES ('two')").Error)

	unmanaged := quotaDB(t)
	cfg := quotaConfig(2, 1)
	cfg.Enabled = false
	require.NoError(t, Install(unmanaged, cfg))
	require.NoError(t, unmanaged.Exec("INSERT INTO organizations(name) VALUES ('one'),('two')").Error)
}

func TestManagedOrganizationConcurrentCreationAndRestore(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, Install(db, quotaConfig(2, 1)))
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- db.Exec("INSERT INTO organizations(name) VALUES ('org')").Error }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			resource, ok := Classify(err)
			require.True(t, ok)
			require.Equal(t, "organizations", resource)
		}
	}
	require.Equal(t, 1, successes)
	require.NoError(t, db.Exec("UPDATE organizations SET name='edited'").Error)
	require.NoError(t, db.Exec("UPDATE organizations SET deleted_at=now()").Error)
	require.NoError(t, db.Exec("INSERT INTO organizations(name) VALUES ('replacement')").Error)
	resource, ok := Classify(db.Exec("UPDATE organizations SET deleted_at=NULL WHERE name='edited'").Error)
	require.True(t, ok)
	require.Equal(t, "organizations", resource)
	require.NoError(t, db.Exec("DELETE FROM organizations WHERE name='replacement'").Error)
	require.NoError(t, db.Exec("UPDATE organizations SET deleted_at=NULL WHERE name='edited'").Error)
	require.NoError(t, Install(db, quotaConfig(2, 1)))
	require.Error(t, db.Exec("INSERT INTO organizations(name) VALUES ('second')").Error)
}

func TestManagedOrganizationAdoptsExistingWithoutDeletion(t *testing.T) {
	db := quotaDB(t)
	require.NoError(t, db.Exec("INSERT INTO organizations(name) VALUES ('one'),('two')").Error)
	require.NoError(t, Install(db, quotaConfig(2, 1)))
	require.NoError(t, db.Exec("UPDATE organizations SET name=name || ' preserved'").Error)
	var count int64
	require.NoError(t, db.Table("organizations").Count(&count).Error)
	require.EqualValues(t, 2, count)
	resource, ok := Classify(db.Exec("INSERT INTO organizations(name) VALUES ('three')").Error)
	require.True(t, ok)
	require.Equal(t, "organizations", resource)
}
