package database_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestInboxLiveRevisionsNotifyOnlyCommittedChangesWithoutContent(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	basePool, err := base.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, basePool.Close()) })
	schema := "live_revisions_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, base.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() { require.NoError(t, base.Exec("DROP SCHEMA "+schema+" CASCADE").Error) })
	configuration, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	configuration.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*configuration)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.InboxCase{}, &models.InboxCaseActivity{}, &models.InboxLiveResource{}, &models.InboxLiveChange{}, &models.InboxFieldDefinition{}, &models.InboxFieldValue{}, &models.InboxSLAObligation{}))
	org, account, contact, agent := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	c := models.InboxCase{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, ContactID: contact, AssigneeID: &agent, Version: 1, Status: models.InboxCaseOpen, Priority: models.InboxPriorityNormal, OpenedAt: time.Now().UTC(), StatusChangedAt: time.Now().UTC()}
	require.NoError(t, db.Create(&c).Error)
	require.NoError(t, database.InstallInboxLiveRevisions(db))
	require.NoError(t, database.InstallInboxLiveRevisions(db))
	var count int64
	require.NoError(t, db.Model(&models.InboxLiveResource{}).Count(&count).Error)
	require.Zero(t, count, "installation fabricates no history for existing cases")
	listener, err := pgx.ConnectConfig(ctx, configuration.Copy())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close(ctx)) })
	_, err = listener.Exec(ctx, "LISTEN whatomate_inbox_live_v1")
	require.NoError(t, err)
	type event struct {
		Protocol int            `json:"v"`
		Org      uuid.UUID      `json:"organization_id"`
		Kind     string         `json:"kind"`
		Resource uuid.UUID      `json:"resource_id"`
		Version  int64          `json:"version"`
		Before   map[string]any `json:"before"`
		After    map[string]any `json:"after"`
	}
	expectedKind, expectedResource := "case", c.ID
	read := func(timeout time.Duration) (event, error) {
		deadline, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		for {
			notice, err := listener.WaitForNotification(deadline)
			if err != nil {
				return event{}, err
			}
			var value event
			if err := json.Unmarshal([]byte(notice.Payload), &value); err != nil {
				return event{}, err
			}
			if value.Org != org {
				continue
			}
			require.Less(t, len(notice.Payload), 1024)
			require.NotContains(t, notice.Payload, "synthetic-private-note")
			require.NotContains(t, notice.Payload, "private-field")
			require.NotContains(t, notice.Payload, "detail")
			require.Equal(t, 1, value.Protocol)
			require.Equal(t, expectedKind, value.Kind)
			require.Equal(t, expectedResource, value.Resource)
			return value, nil
		}
	}
	noEvent := func() {
		t.Helper()
		_, err := read(150 * time.Millisecond)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, listener.IsClosed())
	}
	tx := db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, tx.Model(&c).Update("priority", models.InboxPriorityHigh).Error)
	noEvent()
	require.NoError(t, tx.Rollback().Error)
	noEvent()
	require.NoError(t, db.Model(&models.InboxLiveResource{}).Count(&count).Error)
	require.Zero(t, count, "rollback erases the resource revision too")
	require.NoError(t, db.Exec("UPDATE inbox_cases SET priority='urgent' WHERE id=?", c.ID).Error)
	first, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 1, first.Version)
	require.Equal(t, account.String(), first.After["account_id"])
	require.Equal(t, agent.String(), first.Before["assignee_id"])
	activity := models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, CaseID: c.ID, Kind: "note", Detail: "synthetic-private-note"}
	require.NoError(t, db.Create(&activity).Error)
	second, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 2, second.Version, "activity updates need not mutate the case's API version")
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for _, change := range []string{"priority='high'", "status='pending'"} {
		workers.Go(func() {
			results <- db.Exec("UPDATE inbox_cases SET "+change+" WHERE id=?", c.ID).Error
		})
	}
	workers.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	for _, version := range []int64{3, 4} {
		value, err := read(2 * time.Second)
		require.NoError(t, err)
		require.Equal(t, version, value.Version)
	}
	require.NoError(t, db.Model(&c).Update("priority", models.InboxPriorityHigh).Error)
	noEvent()
	require.NoError(t, listener.Close(ctx))
	listener, err = pgx.ConnectConfig(ctx, configuration.Copy())
	require.NoError(t, err)
	_, err = listener.Exec(ctx, "LISTEN whatomate_inbox_live_v1")
	require.NoError(t, err)
	require.NoError(t, database.InstallInboxLiveRevisions(db))
	require.NoError(t, db.Exec("UPDATE inbox_cases SET priority='low' WHERE id=?", c.ID).Error)
	restarted, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 5, restarted.Version, "reinstall and listener restart do not reset revisions")
	definition := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, Scope: "case", Key: "synthetic_case", Label: "private-field-label", Kind: "text"}
	expectedKind, expectedResource = "configuration", definition.ID
	require.NoError(t, db.Create(&definition).Error)
	defined, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 1, defined.Version)
	value := models.InboxFieldValue{OrganizationID: org, DefinitionID: definition.ID, SubjectID: c.ID, Value: json.RawMessage(`"private-field-value"`)}
	expectedKind, expectedResource = "case", c.ID
	require.NoError(t, db.Create(&value).Error)
	changed, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 6, changed.Version)
	value.Value = json.RawMessage(`"private-field-replacement"`)
	require.NoError(t, db.Save(&value).Error)
	changed, err = read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 7, changed.Version)
	tx = db.Begin()
	require.NoError(t, tx.Delete(&value).Error)
	noEvent()
	require.NoError(t, tx.Rollback().Error)
	noEvent()
	require.NoError(t, db.Delete(&value).Error)
	changed, err = read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 8, changed.Version, "field rollback leaves no revision or notification")
	contactDefinition := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, Scope: "contact", Key: "synthetic_contact", Label: "private-field-contact", Kind: "text"}
	expectedKind, expectedResource = "configuration", contactDefinition.ID
	require.NoError(t, db.Create(&contactDefinition).Error)
	_, err = read(2 * time.Second)
	require.NoError(t, err)
	expectedKind, expectedResource = "contact", contact
	contactValue := models.InboxFieldValue{OrganizationID: org, DefinitionID: contactDefinition.ID, SubjectID: contact, Value: json.RawMessage(`"private-field-contact-value"`)}
	require.NoError(t, db.Create(&contactValue).Error)
	changed, err = read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 1, changed.Version)
	expectedKind, expectedResource = "configuration", definition.ID
	require.NoError(t, db.Model(&definition).Update("archived_at", time.Now().UTC()).Error)
	changed, err = read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 2, changed.Version)
	expectedKind, expectedResource = "case", c.ID
	require.NoError(t, db.Unscoped().Delete(&c).Error)
	removed, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 9, removed.Version)
	require.Equal(t, account.String(), removed.Before["account_id"])
	require.Nil(t, removed.After)
	var resource models.InboxLiveResource
	require.NoError(t, db.First(&resource).Error)
	encoded, err := json.Marshal(resource)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(encoded), "internal routing metadata cannot accidentally be exposed as a DTO")
	// A field change can precede assignment in the same transaction. Coalesce
	// three writes, retaining the first known old owner and final new owner.
	secondCase := c
	secondCase.BaseModel, secondCase.AssigneeID = models.BaseModel{ID: uuid.New()}, &agent
	secondCase.Status, secondCase.Priority = models.InboxCaseOpen, models.InboxPriorityNormal
	expectedResource = secondCase.ID
	require.NoError(t, db.Create(&secondCase).Error)
	created, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 1, created.Version)
	newOwner := uuid.New()
	tx = db.Begin()
	fieldFirst := models.InboxFieldValue{OrganizationID: org, DefinitionID: definition.ID, SubjectID: secondCase.ID, Value: json.RawMessage(`"private-field-transaction"`)}
	require.NoError(t, tx.Create(&fieldFirst).Error)
	require.NoError(t, tx.Exec("UPDATE inbox_cases SET assignee_id=? WHERE id=?", newOwner, secondCase.ID).Error)
	activity.ID, activity.CaseID, activity.AssigneeID = uuid.New(), secondCase.ID, &newOwner
	require.NoError(t, tx.Create(&activity).Error)
	noEvent()
	require.NoError(t, tx.Commit().Error)
	coalesced, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 2, coalesced.Version)
	require.Equal(t, agent.String(), coalesced.Before["assignee_id"])
	require.Equal(t, newOwner.String(), coalesced.After["assignee_id"])
	noEvent()
	// A deadline writer need not lock the case row. Deferred revision locking
	// prevents a new case->revision->deadline / deadline->revision lock cycle.
	deadline := time.Now().UTC().Add(time.Minute)
	obligation := models.InboxSLAObligation{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, CaseID: secondCase.ID, PolicyID: uuid.New(), Kind: "first_reply", StartedAt: time.Now().UTC(), Deadline: &deadline, BudgetMinutes: 1}
	require.NoError(t, db.Create(&obligation).Error)
	_, err = read(2 * time.Second)
	require.NoError(t, err)
	caseWriter := db.Begin()
	defer caseWriter.Rollback()
	require.NoError(t, caseWriter.Model(&secondCase).Update("priority", models.InboxPriorityUrgent).Error)
	deadlineWriter := db.Begin()
	defer deadlineWriter.Rollback()
	require.NoError(t, deadlineWriter.Exec("SET LOCAL lock_timeout='500ms'").Error)
	require.NoError(t, deadlineWriter.Model(&obligation).Update("paused", true).Error, "no live-revision lock is held while the case transaction is unfinished")
	require.NoError(t, deadlineWriter.Commit().Error)
	deadlineChange, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 4, deadlineChange.Version)
	require.NoError(t, caseWriter.Model(&obligation).Update("paused", false).Error)
	require.NoError(t, caseWriter.Commit().Error)
	caseChange, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 5, caseChange.Version)
	bulkStart := time.Now()
	bulk := db.Begin()
	defer bulk.Rollback()
	activities := make([]models.InboxCaseActivity, 1000)
	for i := range activities {
		activities[i] = models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, CaseID: secondCase.ID, Kind: "note", Detail: "synthetic-private-note", AssigneeID: &newOwner}
	}
	require.NoError(t, bulk.CreateInBatches(&activities, 250).Error)
	var dirty string
	require.NoError(t, bulk.Raw("SELECT current_setting('whatomate.inbox_live_'||md5(?),true)", schema).Scan(&dirty).Error)
	require.Equal(t, "1", dirty)
	require.NoError(t, bulk.Commit().Error)
	batched, err := read(2 * time.Second)
	require.NoError(t, err)
	require.EqualValues(t, 6, batched.Version, "1000 writes in one transaction advance this resource once")
	noEvent()
	t.Logf("1000 activity writes, one committed resource invalidation: %v", time.Since(bulkStart))
	require.NoError(t, db.Model(&models.InboxLiveChange{}).Count(&count).Error)
	require.Zero(t, count, "no invalidation staging survives commit or becomes a restored job")
}
