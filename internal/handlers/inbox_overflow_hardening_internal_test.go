package handlers

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestInboxHardeningOverflowStarvesLaterTeam(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	unavailableTeam := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		Name: "Unavailable fixture", IsActive: true, AssignmentStrategy: models.AssignmentStrategyRoundRobin}
	availableTeam := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		Name: "Available fixture", IsActive: true, AssignmentStrategy: models.AssignmentStrategyRoundRobin}
	require.NoError(t, db.Create(&unavailableTeam).Error)
	require.NoError(t, db.Create(&availableTeam).Error)
	user := testutil.CreateTestUser(t, db, org.ID)
	require.NoError(t, db.Model(user).Updates(map[string]any{"is_active": true, "is_available": true}).Error)
	require.NoError(t, db.Where("organization_id = ? AND user_id = ?", org.ID, user.ID).FirstOrCreate(
		&models.UserOrganization{OrganizationID: org.ID, UserID: user.ID}).Error)
	require.NoError(t, db.Create(&models.TeamMember{BaseModel: models.BaseModel{ID: uuid.New()},
		TeamID: availableTeam.ID, UserID: user.ID, Role: models.TeamRoleAgent}).Error)
	var live models.InboxCase
	for i := 0; i < 101; i++ {
		contact := testutil.CreateTestContact(t, db, org.ID)
		team := &unavailableTeam.ID
		if i == 100 {
			team = &availableTeam.ID
		}
		c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC().Add(time.Duration(i)*time.Second), nil, team)
		c.RoutingReason = "unavailable"
		require.NoError(t, db.Create(&c).Error, fmt.Sprint(i))
		if i == 100 {
			live = c
		}
	}
	require.NoError(t, a.routeInboxOverflow(context.Background()))
	require.NoError(t, a.routeInboxOverflow(context.Background()))
	require.NoError(t, db.First(&live, "id = ?", live.ID).Error)
	require.NotNil(t, live.AssigneeID)
	require.Equal(t, user.ID, *live.AssigneeID)
}
