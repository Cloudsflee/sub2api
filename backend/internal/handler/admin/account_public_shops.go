package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

const (
	publicAccountImportShopLinksFileEnv   = "PUBLIC_ACCOUNT_IMPORT_SHOP_LINKS_FILE"
	publicAccountImportShopLinksFile      = "/app/data/public-account-import-shops.json"
	publicAccountImportShopMaxRequestSize = 8 << 10
	publicAccountImportShopMaxStoreSize   = 1 << 20
	publicAccountImportShopMaxNameLength  = 80
	publicAccountImportShopMaxURLLength   = 2048
	publicAccountImportShopMaxCount       = 500
	publicAccountImportShopStoreVersion   = 1
)

var publicAccountImportShopLinksMu sync.Mutex

type PublicAccountImportShop struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
}

type PublicAccountImportShopsResponse struct {
	Shops []PublicAccountImportShop `json:"shops"`
}

type PublicAccountImportShopRequest struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type PublicAccountImportShopSubmission struct {
	Shop    PublicAccountImportShop `json:"shop"`
	Created bool                    `json:"created"`
}

type publicAccountImportShopStore struct {
	Version int                       `json:"version"`
	Shops   []PublicAccountImportShop `json:"shops"`
}

func (h *AccountHandler) ListPublicAccountImportShops(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}

	publicAccountImportShopLinksMu.Lock()
	shops, err := loadPublicAccountImportShops(publicAccountImportShopLinksPath())
	publicAccountImportShopLinksMu.Unlock()
	if err != nil {
		response.InternalError(c, "Failed to load shop links")
		return
	}

	response.Success(c, PublicAccountImportShopsResponse{Shops: shops})
}

func (h *AccountHandler) SubmitPublicAccountImportShop(c *gin.Context) {
	if !publicAccountImportEnabled() {
		response.NotFound(c, "Public account import is disabled")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, publicAccountImportShopMaxRequestSize)
	var req PublicAccountImportShopRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			response.Error(c, http.StatusRequestEntityTooLarge, "Shop submission is too large")
			return
		}
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	name, err := normalizePublicAccountImportShopName(req.Name)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	shopURL, err := normalizePublicAccountImportShopURL(req.URL)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	path := publicAccountImportShopLinksPath()
	publicAccountImportShopLinksMu.Lock()
	defer publicAccountImportShopLinksMu.Unlock()

	shops, err := loadPublicAccountImportShops(path)
	if err != nil {
		response.InternalError(c, "Failed to load shop links")
		return
	}
	for _, shop := range shops {
		if shop.URL == shopURL {
			response.Success(c, PublicAccountImportShopSubmission{Shop: shop, Created: false})
			return
		}
	}
	if len(shops) >= publicAccountImportShopMaxCount {
		response.BadRequest(c, "The shop link list is full")
		return
	}

	shop := PublicAccountImportShop{
		ID:        publicAccountImportShopID(shopURL),
		Name:      name,
		URL:       shopURL,
		CreatedAt: publicAccountImportTimestamp(),
	}
	shops = append([]PublicAccountImportShop{shop}, shops...)
	if err := savePublicAccountImportShops(path, shops); err != nil {
		response.InternalError(c, "Failed to save shop link")
		return
	}

	response.Created(c, PublicAccountImportShopSubmission{Shop: shop, Created: true})
}

func publicAccountImportShopLinksPath() string {
	if path := strings.TrimSpace(os.Getenv(publicAccountImportShopLinksFileEnv)); path != "" {
		return path
	}
	return publicAccountImportShopLinksFile
}

func normalizePublicAccountImportShopName(value string) (string, error) {
	name := strings.Join(strings.Fields(value), " ")
	if name == "" {
		return "", errors.New("shop name is required")
	}
	if utf8.RuneCountInString(name) > publicAccountImportShopMaxNameLength {
		return "", fmt.Errorf("shop name must not exceed %d characters", publicAccountImportShopMaxNameLength)
	}
	return name, nil
}

func normalizePublicAccountImportShopURL(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", errors.New("shop URL is required")
	}
	if len(raw) > publicAccountImportShopMaxURLLength {
		return "", fmt.Errorf("shop URL must not exceed %d characters", publicAccountImportShopMaxURLLength)
	}
	if strings.Contains(raw, "\\") {
		return "", errors.New("shop URL is invalid")
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" {
		return "", errors.New("shop URL is invalid")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("shop URL must use http or https")
	}
	if parsed.User != nil {
		return "", errors.New("shop URL must not contain credentials")
	}

	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", errors.New("shop URL is invalid")
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}

	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", errors.New("shop URL query is invalid")
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if parsed.Path == "/" {
		parsed.Path = ""
		parsed.RawPath = ""
	}

	return parsed.String(), nil
}

func publicAccountImportShopID(shopURL string) string {
	sum := sha256.Sum256([]byte(shopURL))
	return hex.EncodeToString(sum[:12])
}

func publicAccountImportTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func loadPublicAccountImportShops(path string) ([]PublicAccountImportShop, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []PublicAccountImportShop{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > publicAccountImportShopMaxStoreSize {
		return nil, errors.New("shop link store is too large")
	}

	var store publicAccountImportShopStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	if store.Version != publicAccountImportShopStoreVersion {
		return nil, fmt.Errorf("unsupported shop link store version: %d", store.Version)
	}
	if len(store.Shops) > publicAccountImportShopMaxCount {
		return nil, errors.New("shop link store has too many entries")
	}
	if store.Shops == nil {
		store.Shops = []PublicAccountImportShop{}
	}
	return store.Shops, nil
}

func savePublicAccountImportShops(path string, shops []PublicAccountImportShop) error {
	store := publicAccountImportShopStore{Version: publicAccountImportShopStoreVersion, Shops: shops}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".public-account-import-shops-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
