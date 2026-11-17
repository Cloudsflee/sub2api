package productselection

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func testData() Data {
	return Data{Title: "待验证商品", Stage: "review", PriceBasis: "pending", MinimumQuantity: 1}
}

func TestSelectionStorePersistenceIsolationAndConflict(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "selections")}
	records, err := s.List(17)
	require.NoError(t, err)
	require.Empty(t, records)
	_, err = os.Stat(s.Dir)
	require.True(t, errors.Is(err, os.ErrNotExist), "a read must not create storage")
	d := testData()
	d.ProductID, d.ShopID = "product-1", "shop-1"
	first, created, err := s.Create(17, "request-1", d)
	require.NoError(t, err)
	require.True(t, created)
	for _, request := range []string{"request-1", "request-2"} {
		duplicate, created, err := s.Create(17, request, d)
		require.NoError(t, err)
		require.False(t, created)
		require.Equal(t, first, duplicate)
	}
	fresh := Store{Dir: s.Dir}
	records, err = fresh.List(17)
	require.NoError(t, err)
	require.Equal(t, []Record{first}, records)
	records, err = fresh.List(18)
	require.NoError(t, err)
	require.Empty(t, records)
	_, err = fresh.Update(18, first.ID, first.Revision, d)
	require.ErrorIs(t, err, ErrNotFound)
	d.Stage, d.Notes, d.ProductID = "archived", "保留原始快照", "attempt-to-rebind"
	updated, err := fresh.Update(17, first.ID, first.Revision, d)
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Revision)
	require.Equal(t, first.ProductID, updated.ProductID)
	require.Equal(t, first.CreatedAt, updated.CreatedAt)
	_, err = s.Update(17, first.ID, first.Revision, testData())
	require.ErrorIs(t, err, ErrConflict)
	d = updated.Data
	d.Stage = "review"
	restored, err := s.Update(17, updated.ID, updated.Revision, d)
	require.NoError(t, err)
	require.Equal(t, "保留原始快照", restored.Notes)
	require.Equal(t, int64(3), restored.Revision)
}

func TestSelectionStoreFailedSaveAndCorruptFilePreserveData(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	first, _, err := s.Create(17, "one", testData())
	require.NoError(t, err)
	before, err := os.ReadFile(s.path(17))
	require.NoError(t, err)
	s.write = func(string, []byte) error { return errors.New("simulated full disk") }
	d := first.Data
	d.Notes = "should not become visible"
	_, err = s.Update(17, first.ID, first.Revision, d)
	require.Error(t, err)
	_, created, err := s.Create(17, "two", d)
	require.Error(t, err)
	require.False(t, created)
	after, err := os.ReadFile(s.path(17))
	require.NoError(t, err)
	require.Equal(t, before, after)
	records, err := s.List(17)
	require.NoError(t, err)
	require.Equal(t, []Record{first}, records)
	for _, contents := range []string{`{broken`, `{"version":2,"user_id":17}`, `{"version":1,"user_id":18}`} {
		require.NoError(t, os.WriteFile(s.path(17), []byte(contents), 0o600))
		s.write = nil
		_, _, err = s.Create(17, "three", testData())
		require.Error(t, err)
		after, err = os.ReadFile(s.path(17))
		require.NoError(t, err)
		require.Equal(t, contents, string(after))
	}
}

func TestSelectionStoreConcurrentProcesses(t *testing.T) {
	dir := t.TempDir()
	binary, err := os.Executable()
	require.NoError(t, err)
	var commands []*exec.Cmd
	for i := range 2 {
		cmd := exec.Command(binary, "-test.run=^TestSelectionStoreProcessWriter$")
		cmd.Env = append(os.Environ(), "SELECTION_TEST_DIR="+dir, fmt.Sprintf("SELECTION_TEST_PREFIX=writer-%d", i))
		require.NoError(t, cmd.Start())
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		require.NoError(t, cmd.Wait())
	}
	records, err := (Store{Dir: dir}).List(17)
	require.NoError(t, err)
	require.Len(t, records, 30, "overlapping blue/green processes must not lose writes")
}

func TestSelectionStoreProcessWriter(t *testing.T) {
	dir := os.Getenv("SELECTION_TEST_DIR")
	if dir == "" {
		t.Skip("helper process")
	}
	for i := range 15 {
		_, created, err := (Store{Dir: dir}).Create(17, fmt.Sprintf("%s-%d", os.Getenv("SELECTION_TEST_PREFIX"), i), testData())
		require.NoError(t, err)
		require.True(t, created)
	}
}

func TestSelectionValidationAndResearch(t *testing.T) {
	negative, nan, cost := -1.0, math.NaN(), 12.3
	for name, change := range map[string]func(*Data){
		"empty title":         func(d *Data) { d.Title = " " },
		"negative":            func(d *Data) { d.TargetSalePrice = &negative },
		"nan":                 func(d *Data) { d.ObservedSalePrice = &nan },
		"pending cost":        func(d *Data) { d.PurchasePrice = &cost },
		"unverified snapshot": func(d *Data) { d.PurchasePrice = &cost; d.PriceBasis = "snapshot" },
		"fake quote":          func(d *Data) { d.QuoteVerifiedAt = "2026-11-17T00:00:00Z" },
		"script url":          func(d *Data) { d.SourceURL = "javascript:alert(1)" },
		"credentials":         func(d *Data) { d.SourceURL = "https://user:secret@example.com/" },
		"source traversal":    func(d *Data) { d.ShopID = "../../18" },
		"unknown stage":       func(d *Data) { d.Stage = "anything" },
		"duplicate channel":   func(d *Data) { d.Channels = []string{"private", "private"} },
	} {
		t.Run(name, func(t *testing.T) { d := testData(); change(&d); require.Error(t, Validate(&d)) })
	}
	var research struct {
		SampleCount int `json:"sample_count"`
		Profiles    []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Stage    string `json:"stage"`
			Examples []struct {
				Data
				ID string `json:"id"`
			} `json:"examples"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(Research, &research))
	require.Len(t, research.Profiles, 39)
	s := Store{Dir: t.TempDir()}
	count := 0
	for _, p := range research.Profiles {
		d := testData()
		d.Title, d.Stage, d.ResearchID = p.Name, p.Stage, p.ID
		_, _, err := s.Create(17, "direction-"+p.ID, d)
		require.NoError(t, err)
		_, created, err := s.Create(17, "duplicate-"+p.ID, d)
		require.NoError(t, err)
		require.False(t, created)
		for _, sample := range p.Examples {
			d := sample.Data
			d.ProductID, d.ResearchID, d.Stage, d.PriceBasis = sample.ID, p.ID, p.Stage, "snapshot"
			require.NoError(t, Validate(&d), "sample %s", sample.ID)
			count++
		}
	}
	require.Equal(t, 77, count)
	require.Equal(t, count, research.SampleCount)
}
