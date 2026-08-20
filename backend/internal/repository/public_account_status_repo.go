package repository

import (
	"context"
	"fmt"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/accountgroup"
	dbgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type publicAccountStatusRepository struct {
	client *dbent.Client
}

func NewPublicAccountStatusRepository(client *dbent.Client) service.PublicAccountStatusRepository {
	return &publicAccountStatusRepository{client: client}
}

func (r *publicAccountStatusRepository) ListPublicStatusGroups(ctx context.Context) ([]service.PublicStatusGroupRecord, error) {
	rows, err := r.client.Group.Query().
		Where(
			dbgroup.PublicStatusEnabledEQ(true),
			dbgroup.StatusEQ(service.StatusActive),
			dbgroup.PlatformEQ(service.PlatformOpenAI),
			dbgroup.Not(dbgroup.NameEqualFold("ALL")),
		).
		Order(dbent.Asc(dbgroup.FieldSortOrder), dbent.Asc(dbgroup.FieldID)).
		Select(
			dbgroup.FieldID,
			dbgroup.FieldName,
			dbgroup.FieldDescription,
			dbgroup.FieldPlatform,
			dbgroup.FieldStatus,
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.PublicStatusGroupRecord, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		out = append(out, service.PublicStatusGroupRecord{
			ID:          row.ID,
			Name:        row.Name,
			Description: derefString(row.Description),
			Platform:    row.Platform,
			Status:      row.Status,
		})
	}
	return out, nil
}

func (r *publicAccountStatusRepository) ListPublicStatusGroupAccounts(ctx context.Context, groupIDs []int64) ([]service.PublicStatusGroupAccountRecord, error) {
	if len(groupIDs) == 0 {
		return []service.PublicStatusGroupAccountRecord{}, nil
	}
	rows, err := r.client.AccountGroup.Query().
		Where(
			accountgroup.GroupIDIn(groupIDs...),
			accountgroup.HasGroupWith(
				dbgroup.PublicStatusEnabledEQ(true),
				dbgroup.StatusEQ(service.StatusActive),
				dbgroup.PlatformEQ(service.PlatformOpenAI),
				dbgroup.Not(dbgroup.NameEqualFold("ALL")),
			),
		).
		Order(dbent.Asc(accountgroup.FieldGroupID), dbent.Asc(accountgroup.FieldAccountID)).
		WithAccount(func(query *dbent.AccountQuery) {
			query.Select(publicStatusAccountFields()...)
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.PublicStatusGroupAccountRecord, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.Edges.Account == nil {
			continue
		}
		out = append(out, service.PublicStatusGroupAccountRecord{
			GroupID: row.GroupID,
			Account: publicStatusAccountToService(row.Edges.Account),
		})
	}
	return out, nil
}

func (r *publicAccountStatusRepository) ListPublicStatusAccounts(ctx context.Context, groupID int64, offset, limit int) ([]*service.Account, int64, error) {
	exists, err := r.client.Group.Query().
		Where(
			dbgroup.IDEQ(groupID),
			dbgroup.PublicStatusEnabledEQ(true),
			dbgroup.StatusEQ(service.StatusActive),
			dbgroup.PlatformEQ(service.PlatformOpenAI),
			dbgroup.Not(dbgroup.NameEqualFold("ALL")),
		).
		Exist(ctx)
	if err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, service.ErrPublicAccountStatusGroupNotFound
	}

	query := r.client.Account.Query().Where(
		dbaccount.HasAccountGroupsWith(accountgroup.GroupIDEQ(groupID)),
	)
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := query.
		Order(dbent.Asc(dbaccount.FieldID)).
		Offset(offset).
		Limit(limit).
		Select(publicStatusAccountFields()...).
		All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*service.Account, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			out = append(out, publicStatusAccountToService(row))
		}
	}
	if err := r.populateGeminiQuotaHints(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, int64(total), nil
}

type publicStatusGeminiQuotaHint struct {
	ID               int64  `sql:"id"`
	OAuthType        string `sql:"oauth_type"`
	TierID           string `sql:"tier_id"`
	ProjectIDPresent bool   `sql:"project_id_present"`
}

func (r *publicAccountStatusRepository) populateGeminiQuotaHints(ctx context.Context, accounts []*service.Account) error {
	byID := make(map[int64]*service.Account)
	ids := make([]int64, 0)
	for _, account := range accounts {
		if account == nil || account.Platform != service.PlatformGemini || account.Type != service.AccountTypeOAuth {
			continue
		}
		if _, exists := byID[account.ID]; exists {
			continue
		}
		byID[account.ID] = account
		ids = append(ids, account.ID)
	}
	if len(ids) == 0 {
		return nil
	}

	var hints []publicStatusGeminiQuotaHint
	err := r.client.Account.Query().
		Where(dbaccount.IDIn(ids...)).
		Select(dbaccount.FieldID).
		Aggregate(
			publicStatusCredentialText("oauth_type", "oauth_type"),
			publicStatusCredentialText("tier_id", "tier_id"),
			func(selector *entsql.Selector) string {
				credentials := selector.C(dbaccount.FieldCredentials)
				return fmt.Sprintf("COALESCE(%s ->> 'project_id', '') <> '' AS project_id_present", credentials)
			},
		).
		Scan(ctx, &hints)
	if err != nil {
		return err
	}
	for _, hint := range hints {
		account := byID[hint.ID]
		if account == nil {
			continue
		}
		account.GeminiOAuthTypeHint = hint.OAuthType
		account.GeminiTierIDHint = hint.TierID
		account.GeminiProjectIDPresent = hint.ProjectIDPresent
	}
	return nil
}

func publicStatusCredentialText(key, alias string) dbent.AggregateFunc {
	return func(selector *entsql.Selector) string {
		credentials := selector.C(dbaccount.FieldCredentials)
		return fmt.Sprintf("COALESCE(%s ->> '%s', '') AS %s", credentials, key, alias)
	}
}

func publicStatusAccountFields() []string {
	return []string{
		dbaccount.FieldID,
		dbaccount.FieldName,
		dbaccount.FieldPlatform,
		dbaccount.FieldType,
		dbaccount.FieldExtra,
		dbaccount.FieldConcurrency,
		dbaccount.FieldStatus,
		dbaccount.FieldLastUsedAt,
		dbaccount.FieldExpiresAt,
		dbaccount.FieldAutoPauseOnExpired,
		dbaccount.FieldUpdatedAt,
		dbaccount.FieldSchedulable,
		dbaccount.FieldRateLimitedAt,
		dbaccount.FieldRateLimitResetAt,
		dbaccount.FieldOverloadUntil,
		dbaccount.FieldTempUnschedulableUntil,
		dbaccount.FieldSessionWindowStart,
		dbaccount.FieldSessionWindowEnd,
		dbaccount.FieldSessionWindowStatus,
		dbaccount.FieldParentAccountID,
		dbaccount.FieldQuotaDimension,
	}
}

func publicStatusAccountToService(row *dbent.Account) *service.Account {
	if row == nil {
		return nil
	}
	return &service.Account{
		ID:                     row.ID,
		Name:                   row.Name,
		Platform:               row.Platform,
		Type:                   row.Type,
		Extra:                  copyJSONMap(row.Extra),
		Concurrency:            row.Concurrency,
		Status:                 row.Status,
		LastUsedAt:             row.LastUsedAt,
		ExpiresAt:              row.ExpiresAt,
		AutoPauseOnExpired:     row.AutoPauseOnExpired,
		UpdatedAt:              row.UpdatedAt,
		Schedulable:            row.Schedulable,
		RateLimitedAt:          row.RateLimitedAt,
		RateLimitResetAt:       row.RateLimitResetAt,
		OverloadUntil:          row.OverloadUntil,
		TempUnschedulableUntil: row.TempUnschedulableUntil,
		SessionWindowStart:     row.SessionWindowStart,
		SessionWindowEnd:       row.SessionWindowEnd,
		SessionWindowStatus:    derefString(row.SessionWindowStatus),
		ParentAccountID:        row.ParentAccountID,
		QuotaDimension:         string(row.QuotaDimension),
	}
}
