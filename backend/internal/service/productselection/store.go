// Package productselection stores private selection notes separately from the
// public catalog. Reading notes never schedules collection or updates quotes.
package productselection

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxRequestBytes = 128 << 10
	maxStoreBytes   = 8 << 20
	maxRecords      = 300
)

var (
	ErrConflict  = errors.New("selection record has changed; reload before saving")
	ErrNotFound  = errors.New("selection record not found")
	ErrLimit     = errors.New("selection record limit reached")
	ErrRequestID = errors.New("invalid request ID")
	idPattern    = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
)

type Data struct {
	Title              string   `json:"title"`
	ProductID          string   `json:"product_id"`
	ShopID             string   `json:"shop_id"`
	ShopName           string   `json:"shop_name"`
	SourceURL          string   `json:"source_url"`
	Category           string   `json:"category"`
	ResearchID         string   `json:"research_id"`
	Stage              string   `json:"stage"`
	Channels           []string `json:"channels"`
	Tags               []string `json:"tags"`
	PurchasePrice      *float64 `json:"purchase_price"`
	PriceBasis         string   `json:"price_basis"`
	QuoteVerifiedAt    string   `json:"quote_verified_at"`
	MinimumQuantity    int      `json:"minimum_quantity"`
	TargetSalePrice    *float64 `json:"target_sale_price"`
	ObservedSalePrice  *float64 `json:"observed_sale_price"`
	Specification      string   `json:"specification"`
	Customer           string   `json:"customer"`
	OfficialComparison string   `json:"official_comparison"`
	Alternatives       string   `json:"alternatives"`
	InformationGap     string   `json:"information_gap"`
	DeliveryRisk       string   `json:"delivery_risk"`
	AcquisitionPlan    string   `json:"acquisition_plan"`
	Economics          string   `json:"economics"`
	RepeatAssessment   string   `json:"repeat_assessment"`
	Decision           string   `json:"decision"`
	NextAction         string   `json:"next_action"`
	Notes              string   `json:"notes"`
}

type Record struct {
	ID        string `json:"id"`
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	RequestID string `json:"request_id"`
	Data
}

type fileData struct {
	Version int      `json:"version"`
	UserID  int64    `json:"user_id"`
	Records []Record `json:"records"`
}

type Store struct {
	Dir string
	// Tests can force an atomic write failure without changing the old file.
	write func(string, []byte) error
}

func (s Store) path(userID int64) string {
	return filepath.Join(s.Dir, strconv.FormatInt(userID, 10)+".json")
}

func (s Store) List(userID int64) ([]Record, error) {
	if userID <= 0 {
		return nil, errors.New("invalid selection owner")
	}
	f, err := os.Open(s.path(userID))
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxStoreBytes {
		return nil, ErrLimit
	}
	var saved fileData
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if saved.Version != 1 || saved.UserID != userID || len(saved.Records) > maxRecords {
		return nil, errors.New("invalid selection store")
	}
	if saved.Records == nil {
		saved.Records = []Record{}
	}
	return saved.Records, nil
}

func (s Store) mutate(userID int64, change func([]Record) ([]Record, error)) error {
	if userID <= 0 {
		return errors.New("invalid selection owner")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.path(userID)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	// Serialize read/modify/rename across processes during blue/green overlap.
	if err := lockFile(lock); err != nil {
		return err
	}
	defer func() { _ = unlockFile(lock) }()
	records, err := s.List(userID)
	if err != nil {
		return err // Never replace a corrupt/unsupported store with an empty one.
	}
	records, err = change(records)
	if err != nil || records == nil {
		return err // nil records denotes an idempotent no-op.
	}
	data, err := json.MarshalIndent(fileData{Version: 1, UserID: userID, Records: records}, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxStoreBytes {
		return ErrLimit
	}
	write := s.write
	if write == nil {
		write = atomicWrite
	}
	return write(s.path(userID), append(data, '\n'))
}

func (s Store) Create(userID int64, requestID string, data Data) (Record, bool, error) {
	var result Record
	if !idPattern.MatchString(requestID) {
		return result, false, ErrRequestID
	}
	if err := Validate(&data); err != nil {
		return result, false, err
	}
	created := false
	err := s.mutate(userID, func(records []Record) ([]Record, error) {
		for _, r := range records {
			if r.RequestID == requestID || (data.ProductID != "" && r.ProductID == data.ProductID && r.ShopID == data.ShopID) ||
				(data.ProductID == "" && data.ResearchID != "" && r.ProductID == "" && r.ResearchID == data.ResearchID) {
				result = r
				return nil, nil
			}
		}
		if len(records) >= maxRecords {
			return nil, ErrLimit
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		result = Record{ID: hex.EncodeToString(random[:]), Revision: 1, CreatedAt: now, UpdatedAt: now, RequestID: requestID, Data: data}
		created = true
		return append([]Record{result}, records...), nil
	})
	if err != nil {
		return Record{}, false, err
	}
	return result, created, nil
}

func (s Store) Update(userID int64, id string, revision int64, data Data) (Record, error) {
	var result Record
	if !idPattern.MatchString(id) || revision < 1 {
		return result, ErrNotFound
	}
	if err := Validate(&data); err != nil {
		return result, err
	}
	err := s.mutate(userID, func(records []Record) ([]Record, error) {
		for i, r := range records {
			if r.ID != id {
				continue
			}
			if r.Revision != revision {
				return nil, ErrConflict
			}
			// Source identity stays attached to the saved record; notes and prices
			// can evolve without accidentally moving it to another catalog item.
			data.ProductID, data.ShopID, data.ResearchID = r.ProductID, r.ShopID, r.ResearchID
			result = r
			result.Data = data
			result.Revision++
			result.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			records[i] = result
			return records, nil
		}
		return nil, ErrNotFound
	})
	if err != nil {
		return Record{}, err
	}
	return result, nil
}

func Validate(data *Data) error {
	for _, field := range []struct {
		name string
		text *string
		max  int
	}{
		{"title", &data.Title, 300}, {"shop_name", &data.ShopName, 300}, {"category", &data.Category, 150},
		{"specification", &data.Specification, 2500}, {"customer", &data.Customer, 2500},
		{"official_comparison", &data.OfficialComparison, 4000}, {"alternatives", &data.Alternatives, 2500},
		{"information_gap", &data.InformationGap, 2500}, {"delivery_risk", &data.DeliveryRisk, 2500},
		{"acquisition_plan", &data.AcquisitionPlan, 2500}, {"economics", &data.Economics, 2500},
		{"repeat_assessment", &data.RepeatAssessment, 2500}, {"decision", &data.Decision, 2500},
		{"next_action", &data.NextAction, 2500}, {"notes", &data.Notes, 6000},
	} {
		*field.text = strings.TrimSpace(*field.text)
		if !utf8.ValidString(*field.text) || utf8.RuneCountInString(*field.text) > field.max || strings.ContainsRune(*field.text, '\x00') {
			return fmt.Errorf("invalid %s", field.name)
		}
	}
	if data.Title == "" {
		return errors.New("title is required")
	}
	for _, id := range []string{data.ProductID, data.ShopID, data.ResearchID} {
		if id != "" && !idPattern.MatchString(id) {
			return errors.New("invalid source ID")
		}
	}
	switch data.Stage {
	case "review", "priority", "trial", "addon", "hold", "rejected", "archived":
	default:
		return errors.New("invalid selection stage")
	}
	if len(data.Channels) > 4 || len(data.Tags) > 12 {
		return errors.New("too many channels or tags")
	}
	seen := make(map[string]bool)
	for _, c := range data.Channels {
		if (c != "private" && c != "taobao" && c != "xianyu" && c != "xiaohongshu") || seen[c] {
			return errors.New("invalid selection channel")
		}
		seen[c] = true
	}
	for _, tag := range data.Tags {
		if strings.TrimSpace(tag) == "" || utf8.RuneCountInString(tag) > 40 {
			return errors.New("invalid selection tag")
		}
	}
	if data.Channels == nil {
		data.Channels = []string{}
	}
	if data.Tags == nil {
		data.Tags = []string{}
	}
	data.SourceURL = strings.TrimSpace(data.SourceURL)
	if data.SourceURL != "" {
		u, err := url.Parse(data.SourceURL)
		if err != nil || len(data.SourceURL) > 2048 || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("invalid source URL")
		}
	}
	for _, price := range []*float64{data.PurchasePrice, data.TargetSalePrice, data.ObservedSalePrice} {
		if price != nil && (math.IsNaN(*price) || math.IsInf(*price, 0) || *price < 0 || *price > 100000000) {
			return errors.New("invalid price")
		}
	}
	if data.MinimumQuantity < 1 || data.MinimumQuantity > 1000000 {
		return errors.New("invalid minimum quantity")
	}
	switch data.PriceBasis {
	case "pending":
		if data.PurchasePrice != nil || data.QuoteVerifiedAt != "" {
			return errors.New("pending prices must be empty")
		}
	case "estimate":
		if data.PurchasePrice == nil || data.QuoteVerifiedAt != "" {
			return errors.New("estimated prices must not claim quote verification")
		}
	case "snapshot":
		if data.PurchasePrice == nil {
			return errors.New("snapshot price is required")
		}
		if _, err := time.Parse(time.RFC3339Nano, data.QuoteVerifiedAt); err != nil {
			return errors.New("snapshot quote time is required")
		}
	default:
		return errors.New("invalid price basis")
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".selection-*")
	if err != nil {
		return err
	}
	defer func() { _ = temp.Close(); _ = os.Remove(temp.Name()) }()
	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
