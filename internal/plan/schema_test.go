package plan

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestExternalSchemaHandleRemainsReusableAfterMediaRecovery(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "ordinary"
		if external {
			name = "external"
		}
		t.Run(name, func(t *testing.T) {
			base := quotaDB(t)
			require.NoError(t, InstallMediaSchema(base))
			db := base
			if external {
				db = WithExternalSchema(base)
			}
			for i := 0; i < 2; i++ {
				require.NoError(t, RecoverMediaPublications(db, []string{t.TempDir()}))
				var value int
				require.NoError(t, db.Raw("SELECT 1").Scan(&value).Error, "recovery must not retain its closed pinned connection")
				require.Equal(t, 1, value)
				require.NoError(t, VerifyExternalPlan(db, config.PlanConfig{}))
				require.NoError(t, withStorageLock(db, func(conn *gorm.DB) error {
					flag, _ := conn.Get(externalSchemaSetting)
					require.Equal(t, external, flag == true, "fresh storage sessions must retain the verified external setting")
					return nil
				}))
			}
		})
	}
}
