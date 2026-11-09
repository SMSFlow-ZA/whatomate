package aiconnections

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/aiproviders"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

type fixtureProvider func(aiproviders.Request) (aiproviders.Reply, error)

func (f fixtureProvider) Generate(_ context.Context, request aiproviders.Request) (aiproviders.Reply, error) {
	return f(request)
}

func TestConnectionValidationAndAssignment(t *testing.T) {
	db := testutil.SetupTestDB(t)
	require.NoError(t, db.AutoMigrate(&models.ChatbotAIConnection{}, &models.ChatbotAIBinding{}))
	org, other := uuid.New(), uuid.New()
	service, err := New(db, strings.Repeat("k", 32), true, `[{"id":"test","kind":"compatible","base_url":"https://test.example.net/v1"}]`, nil)
	require.NoError(t, err)
	entry := service.endpoints["test"]
	entry.provider = fixtureProvider(func(request aiproviders.Request) (aiproviders.Reply, error) {
		require.Equal(t, "credential-a", request.Credential)
		require.Equal(t, []aiproviders.Message{{Role: "user", Content: probePrompt}}, request.Messages)
		return aiproviders.Reply{Text: "READY"}, nil
	})
	service.endpoints["test"] = entry
	ctx := context.Background()
	conn, err := service.Create(ctx, org, "Test", "test", "fixture-model", "credential-a")
	require.NoError(t, err)
	require.NotContains(t, conn.Credential, "credential-a")
	require.ErrorIs(t, service.Assign(ctx, org, uuid.Nil, conn.ID, false, false), ErrNotReady)
	require.ErrorIs(t, service.Assign(ctx, other, uuid.Nil, conn.ID, false, false), ErrMissing)
	_, err = service.Probe(ctx, org, conn.ID)
	require.NoError(t, err)
	require.NoError(t, service.Assign(ctx, org, uuid.Nil, conn.ID, false, false))
	require.ErrorIs(t, service.Delete(ctx, org, conn.ID), ErrAssigned)
	binding, err := service.Binding(ctx, org, uuid.New())
	require.NoError(t, err)
	require.Equal(t, conn.ID, binding.ConnectionID)
	require.False(t, binding.IncludeHistory)
	require.False(t, binding.IncludeContext)
	require.NoError(t, service.Disable(ctx, org, conn.ID))
	_, err = service.Run(ctx, *binding, 500, func() []aiproviders.Message { t.Fatal("disabled profile must not collect data"); return nil })
	require.ErrorIs(t, err, ErrNotReady)
	require.NoError(t, service.Assign(ctx, org, uuid.Nil, uuid.Nil, false, false))
	require.NoError(t, service.Delete(ctx, org, conn.ID))
}

func TestConnectionLimitsAreNonBlocking(t *testing.T) {
	service := &Service{busy: map[uuid.UUID]bool{}}
	first, second, third := uuid.New(), uuid.New(), uuid.New()
	release, err := service.claim(first)
	require.NoError(t, err)
	_, err = service.claim(first)
	require.ErrorIs(t, err, ErrBusy)
	releaseSecond, err := service.claim(second)
	require.NoError(t, err)
	defer releaseSecond()
	_, err = service.claim(third)
	require.ErrorIs(t, err, ErrBusy)
	release()
	releaseThird, err := service.claim(third)
	require.NoError(t, err)
	releaseThird()
}

func TestProbeDoesNotApproveRotatedCredential(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	org := uuid.New()
	started, release := make(chan struct{}), make(chan struct{})
	s, err := New(db, strings.Repeat("k", 32), true, `[{"id":"test","kind":"compatible","base_url":"https://test.example.net/v1"}]`, func(aiproviders.Target) aiproviders.Provider {
		return fixtureProvider(func(aiproviders.Request) (aiproviders.Reply, error) {
			close(started)
			<-release
			return aiproviders.Reply{Text: "READY"}, nil
		})
	})
	require.NoError(t, err)
	conn, err := s.Create(ctx, org, "Test", "test", "fixture", "old")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := s.Probe(ctx, org, conn.ID); done <- err }()
	<-started
	credential := "new"
	updateErr := s.Update(ctx, org, conn.ID, Update{Credential: &credential})
	close(release)
	require.NoError(t, updateErr)
	require.ErrorIs(t, <-done, ErrChanged)
	require.ErrorIs(t, s.Assign(ctx, org, uuid.Nil, conn.ID, false, false), ErrNotReady)
}

func TestChangingApprovedHostDoesNotReuseStoredCredential(t *testing.T) {
	db := testutil.SetupTestDB(t)
	ctx := context.Background()
	key := strings.Repeat("k", 32)
	org := uuid.New()
	original, err := New(db, key, true, `[{"id":"test","kind":"compatible","base_url":"https://old.example.net/v1"}]`, nil)
	require.NoError(t, err)
	conn, err := original.Create(ctx, org, "Test", "test", "fixture", "old")
	require.NoError(t, err)
	changed, err := New(db, key, true, `[{"id":"test","kind":"compatible","base_url":"https://new.example.net/v1"}]`, func(aiproviders.Target) aiproviders.Provider {
		return fixtureProvider(func(aiproviders.Request) (aiproviders.Reply, error) {
			t.Fatal("old key must not reach the new endpoint")
			return aiproviders.Reply{}, nil
		})
	})
	require.NoError(t, err)
	_, err = changed.Probe(ctx, org, conn.ID)
	require.ErrorIs(t, err, ErrNotReady)
}

func TestSettingsCanBeEnabledWithoutApprovedEndpoints(t *testing.T) {
	s, err := New(nil, strings.Repeat("k", 32), true, "[]", nil)
	require.NoError(t, err)
	require.True(t, s.Enabled())
	require.Empty(t, s.Options())
	_, err = s.Create(context.Background(), uuid.New(), "Test", "unapproved", "fixture", "key")
	require.ErrorIs(t, err, aiproviders.ErrInvalidRequest)
}
