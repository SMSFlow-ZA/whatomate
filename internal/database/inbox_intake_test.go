package database_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestInboxIntakeMutationStampsSurviveClearingRollbackAndOldWriters(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	schema := "intake_stamps_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, tx.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, tx.Exec("SET LOCAL search_path TO "+schema).Error)
	require.NoError(t, tx.AutoMigrate(&models.InboxFieldValue{}, &models.InboxFieldStamp{}))
	value := models.InboxFieldValue{OrganizationID: uuid.New(), DefinitionID: uuid.New(), SubjectID: uuid.New(), Value: json.RawMessage(`"legacy"`)}
	require.NoError(t, tx.Create(&value).Error)
	require.NoError(t, database.InstallInboxFieldStamps(tx))
	require.NoError(t, database.InstallInboxFieldStamps(tx))
	read := func(subject uuid.UUID) int64 {
		var stamp models.InboxFieldStamp
		err := tx.Where("organization_id=? AND definition_id=? AND subject_id=?", value.OrganizationID, value.DefinitionID, subject).First(&stamp).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0
		}
		require.NoError(t, err)
		return stamp.Revision
	}
	require.Zero(t, read(value.SubjectID), "migration must not fabricate a human edit")
	where := func(db *gorm.DB) *gorm.DB {
		return db.Model(&models.InboxFieldValue{}).Where("organization_id=? AND definition_id=? AND subject_id=?", value.OrganizationID, value.DefinitionID, value.SubjectID)
	}
	// Raw SQL models the previous image: no knowledge of a stamp API is needed.
	require.NoError(t, where(tx).Update("value", json.RawMessage(`"newer"`)).Error)
	require.EqualValues(t, 1, read(value.SubjectID))
	abort := errors.New("synthetic rollback")
	require.ErrorIs(t, tx.Transaction(func(inner *gorm.DB) error {
		require.NoError(t, where(inner).Update("value", json.RawMessage(`"rolled back"`)).Error)
		return abort
	}), abort)
	require.EqualValues(t, 1, read(value.SubjectID))
	require.NoError(t, where(tx).Delete(&models.InboxFieldValue{}).Error)
	require.EqualValues(t, 2, read(value.SubjectID))
	require.NoError(t, tx.Create(&value).Error)
	require.EqualValues(t, 3, read(value.SubjectID), "clear and recreate cannot reset an intake fence")
	newSubject := uuid.New()
	require.NoError(t, where(tx).Update("subject_id", newSubject).Error)
	require.EqualValues(t, 4, read(value.SubjectID))
	require.EqualValues(t, 1, read(newSubject), "key moves fence both subjects")
	require.NoError(t, database.InstallInboxFieldStamps(tx))
	require.EqualValues(t, 4, read(value.SubjectID), "reapplying migration preserves stamps")
}
