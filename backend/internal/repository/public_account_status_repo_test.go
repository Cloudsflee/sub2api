package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestPopulatePublicStatusGeminiQuotaHintsUsesNarrowProjection(t *testing.T) {
	var capturedSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &capturedSQL}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	repo := &publicAccountStatusRepository{client: client}

	mock.ExpectQuery("public Gemini quota hints").
		WithArgs(int64(81)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "oauth_type", "tier_id", "project_id_present"}).
			AddRow(int64(81), "google_one", "google_ai_pro", true))
	account := &service.Account{
		ID:       81,
		Platform: service.PlatformGemini,
		Type:     service.AccountTypeOAuth,
	}

	err = repo.populateGeminiQuotaHints(context.Background(), []*service.Account{account})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, "google_one", account.GeminiOAuthTypeHint)
	require.Equal(t, "google_ai_pro", account.GeminiTierIDHint)
	require.True(t, account.GeminiProjectIDPresent)
	require.Nil(t, account.Credentials)

	normalized := normalizeSQLWhitespace(capturedSQL)
	selectClause, _, found := strings.Cut(normalized, " FROM ")
	require.True(t, found, "unexpected projection SQL: %s", normalized)
	for _, field := range []string{"oauth_type", "tier_id", "project_id"} {
		require.Contains(t, selectClause, "->> '"+field+"'")
	}
	require.NotContains(t, selectClause, "access_token")
	require.NotContains(t, selectClause, "refresh_token")
	require.NotContains(t, selectClause, "api_key")
}
