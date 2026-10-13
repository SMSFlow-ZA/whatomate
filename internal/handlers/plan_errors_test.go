package handlers

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSendPlanErrorMapsDatabaseConstraints(t *testing.T) {
	for _, tc := range []struct {
		constraint string
		status     int
		message    string
	}{
		{"whatomate_plan_users_limit", 409, "User limit reached"},
		{"whatomate_plan_numbers_limit", 409, "WhatsApp number limit reached"},
		{"whatomate_plan_support_protected", 403, "Support account is protected"},
	} {
		t.Run(tc.constraint, func(t *testing.T) {
			r := testutil.NewRequest(t)
			handled, err := (&App{}).sendPlanError(r, &pgconn.PgError{Code: "23514", ConstraintName: tc.constraint})
			require.True(t, handled)
			require.NoError(t, err)
			testutil.AssertErrorResponse(t, r, tc.status, tc.message)
		})
	}
	r := testutil.NewRequest(t)
	handled, err := (&App{}).sendPlanError(r, errors.New("unrelated database failure"))
	require.False(t, handled)
	require.NoError(t, err)
	require.Empty(t, r.RequestCtx.Response.Body())
}
