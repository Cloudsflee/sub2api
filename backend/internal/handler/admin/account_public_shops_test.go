package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPublicAccountImportShopsSubmitDeduplicatesAndPersists(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	storePath := filepath.Join(t.TempDir(), "shops.json")
	t.Setenv(publicAccountImportShopLinksFileEnv, storePath)

	h := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/shops", h.ListPublicAccountImportShops)
	router.POST("/shops", h.SubmitPublicAccountImportShop)

	first := performPublicShopRequest(t, router, http.MethodPost, `/shops`, PublicAccountImportShopRequest{
		Name: "  Example   Shop  ",
		URL:  "HTTPS://Example.COM:443/store?b=2&a=1#offer",
	})
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	var firstPayload struct {
		Data PublicAccountImportShopSubmission `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstPayload))
	require.True(t, firstPayload.Data.Created)
	require.Equal(t, "Example Shop", firstPayload.Data.Shop.Name)
	require.Equal(t, "https://example.com/store?a=1&b=2", firstPayload.Data.Shop.URL)

	duplicate := performPublicShopRequest(t, router, http.MethodPost, `/shops`, PublicAccountImportShopRequest{
		Name: "Different name",
		URL:  "https://example.com:443/store?a=1&b=2",
	})
	require.Equal(t, http.StatusOK, duplicate.Code, duplicate.Body.String())
	var duplicatePayload struct {
		Data PublicAccountImportShopSubmission `json:"data"`
	}
	require.NoError(t, json.Unmarshal(duplicate.Body.Bytes(), &duplicatePayload))
	require.False(t, duplicatePayload.Data.Created)
	require.Equal(t, firstPayload.Data.Shop, duplicatePayload.Data.Shop)

	list := performPublicShopRequest(t, router, http.MethodGet, `/shops`, nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var listPayload struct {
		Data PublicAccountImportShopsResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listPayload))
	require.Equal(t, []PublicAccountImportShop{firstPayload.Data.Shop}, listPayload.Data.Shops)

	data, err := os.ReadFile(storePath)
	require.NoError(t, err)
	require.True(t, json.Valid(data))
	require.Equal(t, os.FileMode(0o600), requireFileMode(t, storePath))
}

func TestPublicAccountImportShopsRejectsUnsafeURLs(t *testing.T) {
	t.Setenv(publicAccountImportEnabledEnv, "true")
	t.Setenv(publicAccountImportShopLinksFileEnv, filepath.Join(t.TempDir(), "shops.json"))

	h := NewAccountHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/shops", h.SubmitPublicAccountImportShop)

	for _, shopURL := range []string{
		"javascript:alert(1)",
		"https://user:password@example.com/store",
		"https://example.com\\@attacker.test/store",
	} {
		recorder := performPublicShopRequest(t, router, http.MethodPost, `/shops`, PublicAccountImportShopRequest{
			Name: "Unsafe",
			URL:  shopURL,
		})
		require.Equal(t, http.StatusBadRequest, recorder.Code, shopURL)
	}
}

func performPublicShopRequest(t *testing.T, router http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		require.NoError(t, err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func requireFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Mode().Perm()
}
