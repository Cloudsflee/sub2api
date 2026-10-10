//go:build integration

package repository

import (
	"context"
	"database/sql"
	"io/fs"
	"net/url"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// The custom production baseline already includes every SQL migration except
// 243. Exercise the actual runner on an isolated database, including its
// checksum ledger and the older runner's view of the upgraded schema.
func TestCustomV0215MigrationUpgradeReplayAndRollback(t *testing.T) {
	ctx := context.Background()
	baseline := fstest.MapFS{}
	entries, err := fs.ReadDir(dbmigrations.FS, ".")
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() == "243_drop_platform_check_constraints.sql" {
			continue
		}
		body, readErr := dbmigrations.FS.ReadFile(entry.Name())
		require.NoError(t, readErr)
		baseline[entry.Name()] = &fstest.MapFile{Data: body}
	}
	require.Len(t, baseline, 299, "update this explicit production-baseline boundary for future upgrades")

	for _, fresh := range []bool{false, true} {
		t.Run(strconv.FormatBool(fresh), func(t *testing.T) {
			name := "v0215_migration_" + strconv.FormatInt(time.Now().UnixNano(), 10)
			_, err := integrationDB.ExecContext(ctx, "CREATE DATABASE "+name)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, dropErr := integrationDB.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
				require.NoError(t, dropErr)
			})
			dsn, err := url.Parse(integrationDSN)
			require.NoError(t, err)
			dsn.Path = "/" + name
			db, err := sql.Open("postgres", dsn.String())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			if fresh {
				require.NoError(t, ApplyMigrations(ctx, db))
			} else {
				require.NoError(t, applyMigrationsFS(ctx, db, baseline))
			}
			var userID int64
			require.NoError(t, db.QueryRowContext(ctx, `INSERT INTO users (email, password_hash, balance)
VALUES ('upgrade-fixture@example.invalid', 'synthetic-not-a-password', 7) RETURNING id`).Scan(&userID))
			_, err = db.ExecContext(ctx, `INSERT INTO user_platform_quotas
(user_id, platform, daily_limit_usd, daily_usage_usd, weekly_usage_usd, monthly_usage_usd)
VALUES ($1, 'openai', 12, 3, 4, 5)`, userID)
			require.NoError(t, err)
			for range 2 {
				require.NoError(t, ApplyMigrations(ctx, db))
			}
			// Starting the previous version must still accept its immutable ledger.
			require.NoError(t, applyMigrationsFS(ctx, db, baseline))
			var balance, limit, daily, weekly, monthly float64
			require.NoError(t, db.QueryRowContext(ctx, `SELECT u.balance, q.daily_limit_usd,
q.daily_usage_usd, q.weekly_usage_usd, q.monthly_usage_usd
FROM users u JOIN user_platform_quotas q ON q.user_id = u.id WHERE u.id = $1`, userID).
				Scan(&balance, &limit, &daily, &weekly, &monthly))
			require.Equal(t, []float64{7, 12, 3, 4, 5}, []float64{balance, limit, daily, weekly, monthly})
			var count int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count))
			require.Equal(t, 300, count)
		})
	}
}
