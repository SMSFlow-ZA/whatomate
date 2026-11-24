package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxAuditAdditiveUpgradePreservesLegacySQLAndRequiresDedupIndex(t *testing.T) {
	f := newInboxAuditFixture(t)
	schema := "audit_upgrade_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	rollback := errors.New("owned audit migration rehearsal rollback")
	err := f.db.Transaction(func(tx *gorm.DB) error {
		// The schema name is only a fixed prefix and generated UUID hex. The
		// transaction owns all DDL; no shared table/index is modified or dropped.
		if err := tx.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL search_path TO " + schema + ", public").Error; err != nil {
			return err
		}
		if err := tx.Exec(`CREATE TABLE audit_logs (
id uuid PRIMARY KEY DEFAULT gen_random_uuid(),organization_id uuid NOT NULL,
resource_type varchar(50) NOT NULL,resource_id uuid NOT NULL,user_id uuid NOT NULL,
user_name varchar(255) NOT NULL,action varchar(20) NOT NULL,changes jsonb DEFAULT '[]',
created_at timestamptz DEFAULT now())`).Error; err != nil {
			return err
		}
		oldWrite := func() error {
			return tx.Exec("INSERT INTO audit_logs(organization_id,resource_type,resource_id,user_id,user_name,action) VALUES (?,'user',?,?,'Synthetic legacy operator','updated')", f.org.ID, f.user.ID, f.user.ID).Error
		}
		if err := oldWrite(); err != nil {
			return err
		}
		if err := oldWrite(); err != nil {
			return err
		}
		if err := tx.AutoMigrate(&models.AuditLog{}); err != nil {
			return err
		}
		if err := tx.AutoMigrate(&models.AuditLog{}); err != nil {
			return err
		}
		// The previous schema's known-column write still works after migration.
		if err := oldWrite(); err != nil {
			return err
		}
		var legacy []models.AuditLog
		if err := tx.Order("created_at,id").Find(&legacy).Error; err != nil {
			return err
		}
		require.Len(t, legacy, 3)
		for _, row := range legacy {
			require.Zero(t, row.ContextVersion)
			require.Empty(t, row.RequestID)
			require.Empty(t, row.OriginIP)
			require.Empty(t, row.Outcome)
			require.False(t, row.ActorVerified)
			require.Equal(t, "configuration", row.Category)
		}
		if err := tx.Exec("DROP INDEX " + schema + ".idx_audit_request_event").Error; err != nil {
			return err
		}
		event := f.event()
		missing := tx.Transaction(func(nested *gorm.DB) error { return audit.RecordSecurity(nested, event) })
		require.Error(t, missing)
		require.Equal(t, "database_42P10", audit.ErrorCode(missing))
		var count int64
		if err := tx.Model(&models.AuditLog{}).Where("context_version=1").Count(&count).Error; err != nil {
			return err
		}
		require.Zero(t, count)
		if err := tx.AutoMigrate(&models.AuditLog{}); err != nil {
			return err
		}
		if err := audit.RecordSecurity(tx, event); err != nil {
			return err
		}
		if err := audit.RecordSecurity(tx, event); err != nil {
			return err
		}
		if err := tx.Model(&models.AuditLog{}).Where("context_version=1").Count(&count).Error; err != nil {
			return err
		}
		require.EqualValues(t, 1, count)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var schemas int64
	require.NoError(t, f.db.Raw("SELECT count(*) FROM pg_namespace WHERE nspname=?", schema).Scan(&schemas).Error)
	require.Zero(t, schemas)
}
