package database_test

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"os"
	"strings"
	"testing"
)

func TestChannelMigrationCreatesFreshDatabaseAndReapplies(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	schema := "channel_fresh_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, tx.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, tx.Exec("SET LOCAL search_path TO "+schema).Error)
	require.NoError(t, database.AutoMigrate(tx))
	require.NoError(t, database.AutoMigrate(tx))
	require.True(t, tx.Migrator().HasTable(&models.ChannelAttempt{}))
	var required bool
	require.NoError(t, tx.Raw("SELECT attnotnull FROM pg_attribute WHERE attrelid='bulk_message_campaigns'::regclass AND attname='template_id'").Scan(&required).Error)
	require.False(t, required)
}
