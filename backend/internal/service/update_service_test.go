//go:build unit

package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type updateServiceCacheStub struct {
	data string
}

func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

type updateServiceGitHubClientStub struct {
	release        *GitHubRelease
	recentReleases []*GitHubRelease
	recentErr      error
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(context.Context, string) (*GitHubRelease, error) {
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) FetchRecentReleases(context.Context, string, int) ([]*GitHubRelease, error) {
	return s.recentReleases, s.recentErr
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	_, err := svc.PerformUpdate(context.Background())

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

func TestManagedBuildUsesOfficialBaseVersionForUpdateChecks(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.153"}},
		"0.1.153-custom.4cf0672931f6",
		"release",
	)

	info, err := svc.CheckUpdate(context.Background(), true)

	require.NoError(t, err)
	require.False(t, info.HasUpdate)
	require.Equal(t, managedBuildType, info.BuildType)
}

func TestManagedBuildQueuesRepositorySyncWithoutDownloadingBinary(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.154"}},
		"0.1.153-custom.4cf0672931f6",
		"release",
	)
	dir := t.TempDir()
	svc.managedUpdateRequestFile = dir + "/upstream-sync-request"
	svc.managedUpdateStatusFile = dir + "/upstream-sync-status"

	result, err := svc.PerformUpdate(context.Background())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Queued)
	require.False(t, result.NeedRestart)
	require.Equal(t, "0.1.154", result.TargetVersion)
	request, err := os.ReadFile(svc.managedUpdateRequestFile)
	require.NoError(t, err)
	require.Equal(t, "v0.1.154\n", string(request))
	status, err := svc.readManagedUpdateStatus()
	require.NoError(t, err)
	require.Equal(t, &ManagedUpdateStatus{
		Status:        managedUpdateStatusQueued,
		TargetVersion: "0.1.154",
		Message:       "waiting for the repository sync worker",
		UpdatedAt:     status.UpdatedAt,
	}, status)
	require.NotEmpty(t, status.UpdatedAt)
}

func TestManagedBuildReturnsPersistedRepositorySyncFailure(t *testing.T) {
	dir := t.TempDir()
	statusFile := dir + "/upstream-sync-status"
	require.NoError(t, os.WriteFile(statusFile, []byte(
		"status=failed\n"+
			"target=v0.1.155\n"+
			"commit=abc123\n"+
			"message=sync repository not found\n"+
			"updated_at=2026-07-14T08:54:37Z\n",
	), 0o644))
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.155"}},
		"0.1.153-custom.a886bae16bd1",
		"release",
	)
	svc.managedUpdateStatusFile = statusFile

	info, err := svc.CheckUpdate(context.Background(), true)

	require.NoError(t, err)
	require.True(t, info.HasUpdate)
	require.Equal(t, &ManagedUpdateStatus{
		Status:        managedUpdateStatusFailed,
		TargetVersion: "0.1.155",
		Commit:        "abc123",
		Message:       "sync repository not found",
		UpdatedAt:     "2026-07-14T08:54:37Z",
	}, info.ManagedUpdate)
}

func TestManagedBuildDoesNotQueueDuplicateActiveRepositorySync(t *testing.T) {
	dir := t.TempDir()
	statusFile := dir + "/upstream-sync-status"
	require.NoError(t, os.WriteFile(statusFile, []byte(
		"status=processing\n"+
			"target=v0.1.155\n"+
			"commit=\n"+
			"message=fetching repositories\n"+
			"updated_at=2026-07-14T09:00:00Z\n",
	), 0o644))
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.155"}},
		"0.1.153-custom.a886bae16bd1",
		"release",
	)
	svc.managedUpdateStatusFile = statusFile
	svc.managedUpdateRequestFile = dir + "/upstream-sync-request"

	result, err := svc.PerformUpdate(context.Background())

	require.NoError(t, err)
	require.True(t, result.Queued)
	require.Equal(t, "0.1.155", result.TargetVersion)
	_, err = os.Stat(svc.managedUpdateRequestFile)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestManagedBuildRejectsInPlaceRollback(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{},
		"0.1.153-custom.4cf0672931f6",
		"release",
	)

	require.ErrorIs(t, svc.Rollback(), ErrManagedRollbackRequired)
	require.ErrorIs(t, svc.RollbackToVersion(context.Background(), "0.1.152"), ErrManagedRollbackRequired)
	versions, err := svc.ListRollbackVersions(context.Background())
	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestCompareVersionsIgnoresManagedBuildSuffix(t *testing.T) {
	require.Zero(t, compareVersions("0.1.153-custom.4cf0672931f6", "0.1.153"))
	require.Less(t, compareVersions("0.1.153-custom.4cf0672931f6", "0.1.154"), 0)
}

func newRollbackTestService(current string, releases []*GitHubRelease) *UpdateService {
	return NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

func TestUpdateServiceListRollbackVersionsFiltersAndCaps(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148", PublishedAt: "2026-07-09T00:00:00Z"},                       // newer than current: excluded
		{TagName: "v0.1.147", PublishedAt: "2026-07-08T00:00:00Z"},                       // current: excluded
		{TagName: "v0.1.146-rc1", PublishedAt: "2026-07-07T12:00:00Z", Prerelease: true}, // prerelease: excluded
		{TagName: "v0.1.146", PublishedAt: "2026-07-07T00:00:00Z"},
		{TagName: "v0.1.145", PublishedAt: "2026-07-06T00:00:00Z", Draft: true}, // draft: excluded
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"},
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"}, // duplicate: excluded
		{TagName: "v0.1.143", PublishedAt: "2026-07-04T00:00:00Z"},
		{TagName: "v0.1.142", PublishedAt: "2026-07-03T00:00:00Z"}, // beyond cap of 3: excluded
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.144", versions[1].Version)
	require.Equal(t, "0.1.143", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsSortsUnorderedInput(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.144"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.145", versions[1].Version)
	require.Equal(t, "0.1.144", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsEmptyWhenNoneOlder(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.148"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestUpdateServiceListRollbackVersionsPropagatesFetchError(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentErr: errors.New("github unavailable")},
		"0.1.147",
		"release",
	)

	_, err := svc.ListRollbackVersions(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "github unavailable")
}

func TestUpdateServiceRollbackToVersionRejectsDisallowedTargets(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148"},
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
		{TagName: "v0.1.144"},
		{TagName: "v0.1.143"},
		{TagName: "v0.1.142"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	for _, target := range []string{
		"",         // empty
		"0.1.147",  // current version
		"v0.1.147", // current version with prefix
		"0.1.148",  // newer than current
		"0.1.142",  // older than the 3 most recent
		"9.9.9",    // nonexistent
	} {
		err := svc.RollbackToVersion(context.Background(), target)
		require.ErrorIs(t, err, ErrRollbackVersionNotAllowed, "target %q should be rejected", target)
	}
}

func TestUpdateServiceRollbackToVersionAcceptsVPrefix(t *testing.T) {
	// No platform asset in the release: the target passes the allowlist check
	// and fails later at asset lookup, proving the version itself was accepted.
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	err := svc.RollbackToVersion(context.Background(), "v0.1.146")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRollbackVersionNotAllowed)
	require.Contains(t, err.Error(), "no compatible release found")
}
