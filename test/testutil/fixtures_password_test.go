package testutil

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestDefaultFixturePasswordRetainsCostAndConcurrentConsistency(t *testing.T) {
	const count = 32
	hashes := make([]string, count)
	errors := make([]error, count)
	var workers sync.WaitGroup
	for i := range count {
		workers.Go(func() { hashes[i], errors[i] = defaultTestUserPasswordHash() })
	}
	workers.Wait()
	for i := range count {
		require.NoError(t, errors[i])
		require.Equal(t, hashes[0], hashes[i])
	}
	cost, err := bcrypt.Cost([]byte(hashes[0]))
	require.NoError(t, err)
	require.Equal(t, bcrypt.DefaultCost, cost)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hashes[0]), []byte("password123")))
	require.ErrorIs(t, bcrypt.CompareHashAndPassword([]byte(hashes[0]), []byte("different-synthetic-password")), bcrypt.ErrMismatchedHashAndPassword)
}
