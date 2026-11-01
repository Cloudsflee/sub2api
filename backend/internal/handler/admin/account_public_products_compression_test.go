package admin

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPublicProductCatalogCompressionPreservesDataAndConditionalRequests(t *testing.T) {
	shop := PublicAccountImportShop{ID: "shop", URL: "https://wzyp.cn/shop/shop"}
	cached := authoritativeProductCache(shop.ID, time.Now().UTC().Add(-time.Minute), 2)
	cached.Products[0].Name = strings.Repeat("Product title ", 100)
	router := newPublicProductTestRouter(t, []PublicAccountImportShop{shop}, publicAccountImportProductStore{
		Shops: map[string]publicAccountImportProductShopCache{shop.ID: cached},
	})
	get := func(encoding, etag string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/products", nil)
		request.Header.Set("Accept-Encoding", encoding)
		request.Header.Set("If-None-Match", etag)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}
	plain := get("identity", "")
	require.Equal(t, http.StatusOK, plain.Code)
	require.Empty(t, plain.Header().Get("Content-Encoding"))
	require.Equal(t, "Accept-Encoding", plain.Header().Get("Vary"))
	compressed := get("gzip, deflate, br", plain.Header().Get("ETag"))
	require.Equal(t, http.StatusOK, compressed.Code)
	require.Equal(t, "gzip", compressed.Header().Get("Content-Encoding"))
	require.Equal(t, "Accept-Encoding", compressed.Header().Get("Vary"))
	require.Less(t, compressed.Body.Len(), plain.Body.Len())
	reader, err := gzip.NewReader(bytes.NewReader(compressed.Body.Bytes()))
	require.NoError(t, err)
	decoded, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.JSONEq(t, plain.Body.String(), string(decoded))
	// Strong ETags identify the actual encoded representation.
	require.NotEqual(t, plain.Header().Get("ETag"), compressed.Header().Get("ETag"))
	unchanged := get("gzip", compressed.Header().Get("ETag"))
	require.Equal(t, http.StatusNotModified, unchanged.Code)
	require.Empty(t, unchanged.Body.Bytes())
	require.Equal(t, compressed.Header().Get("ETag"), unchanged.Header().Get("ETag"))
	require.Equal(t, "Accept-Encoding", unchanged.Header().Get("Vary"))
	disabled := get("gzip;q=0, *;q=1", "")
	require.Empty(t, disabled.Header().Get("Content-Encoding"))
	require.Equal(t, plain.Body.String(), disabled.Body.String())
}

func TestPublicProductCatalogGzipNegotiation(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"", false}, {"br, deflate", false}, {"gzip", true},
		{"br, GZIP; q=0.5", true}, {"gzip;q=0", false},
		{"*;q=1", true}, {"*;q=1, gzip;q=0", false},
		{"gzip;q=0, *;q=1", false}, {"gzip;q=invalid", false},
		{"gzip;q=NaN", false}, {"gzip;q=2", false}, {"gzip;q=-1", false},
	} {
		t.Run(tc.header, func(t *testing.T) {
			require.Equal(t, tc.want, publicAccountImportProductsAcceptsGzip(tc.header))
		})
	}
}
