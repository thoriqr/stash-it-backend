package testutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyBaseline(t *testing.T) {
	container := NewPostgres(t)
	pool := NewPostgresPool(t, container)

	ApplyBaseline(t, pool)

	var tableExists bool

	err := pool.QueryRow(
		context.Background(),
		`
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name = 'users'
		)
		`,
	).Scan(&tableExists)

	require.NoError(t, err)
	require.True(t, tableExists)
}