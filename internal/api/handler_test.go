package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/version-to-hash/version-to-hash/internal/cache"
	"github.com/version-to-hash/version-to-hash/internal/github"
)

func init() {
	gin.SetMode(gin.TestMode)
}

const fixedHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// stubGitHub returns an httptest.Server that always resolves tag refs to fixedHash.
func stubGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/ref/tags/") {
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{"type": "commit", "sha": fixedHash},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setupRouter(t *testing.T, ghSrv *httptest.Server) *gin.Engine {
	t.Helper()

	c, err := cache.New(filepath.Join(t.TempDir(), "test.db"), time.Hour)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	ghClient := github.NewClientWithBaseURL("", ghSrv.URL)

	log := logrus.New()
	log.SetOutput(io.Discard)

	h := NewHandler(c, ghClient, log)
	r := gin.New()
	h.RegisterRoutes(r)
	return r
}

func TestHealthEndpoint(t *testing.T) {
	ghSrv := stubGitHub(t)
	r := setupRouter(t, ghSrv)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestResolveGET_MissingParam(t *testing.T) {
	ghSrv := stubGitHub(t)
	r := setupRouter(t, ghSrv)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/resolve", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestResolveGET_Success(t *testing.T) {
	ghSrv := stubGitHub(t)
	r := setupRouter(t, ghSrv)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/resolve?action=actions/checkout@v4", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp resolveResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Hash != fixedHash {
		t.Errorf("hash = %q, want %q", resp.Hash, fixedHash)
	}
	if resp.Resolved != "actions/checkout@"+fixedHash {
		t.Errorf("resolved = %q", resp.Resolved)
	}
	if resp.Cached {
		t.Error("first request should not be cached")
	}
}

func TestResolveGET_CacheHit(t *testing.T) {
	ghSrv := stubGitHub(t)
	r := setupRouter(t, ghSrv)

	// First request populates the cache.
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/resolve?action=actions/checkout@v4", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i, w.Code)
		}

		var resp resolveResponse
		json.NewDecoder(w.Body).Decode(&resp)
		if i == 1 && !resp.Cached {
			t.Error("second request should be a cache hit")
		}
	}
}

func TestResolvePOST_Success(t *testing.T) {
	ghSrv := stubGitHub(t)
	r := setupRouter(t, ghSrv)

	body := strings.NewReader(`{"action":"actions/setup-python@v5"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/resolve", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp resolveResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Hash != fixedHash {
		t.Errorf("hash = %q, want %q", resp.Hash, fixedHash)
	}
}

func TestLatestGET_Success(t *testing.T) {
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/actions/checkout/releases/latest":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v4.2.2"})
		case "/repos/actions/checkout/git/ref/tags/v4.2.2":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{"type": "commit", "sha": fixedHash},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ghSrv.Close)
	r := setupRouter(t, ghSrv)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/latest?action=actions/checkout", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp latestResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Tag != "v4.2.2" {
		t.Errorf("tag = %q, want %q", resp.Tag, "v4.2.2")
	}
	if resp.Hash != fixedHash {
		t.Errorf("hash = %q, want %q", resp.Hash, fixedHash)
	}
	if resp.Resolved != "actions/checkout@"+fixedHash {
		t.Errorf("resolved = %q", resp.Resolved)
	}
}

func TestLatestGET_VersionSuffixIgnored(t *testing.T) {
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/actions/checkout/releases/latest":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v4.2.2"})
		case "/repos/actions/checkout/git/ref/tags/v4.2.2":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{"type": "commit", "sha": fixedHash},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ghSrv.Close)
	r := setupRouter(t, ghSrv)

	w := httptest.NewRecorder()
	// @v3 suffix should be ignored; latest (v4.2.2) is returned.
	req := httptest.NewRequest(http.MethodGet, "/latest?action=actions/checkout@v3", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp latestResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Tag != "v4.2.2" {
		t.Errorf("tag = %q, want v4.2.2", resp.Tag)
	}
}

func TestLatestGET_MissingParam(t *testing.T) {
	r := setupRouter(t, stubGitHub(t))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/latest", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPinned(t *testing.T) {
	tests := []struct {
		action string
		hash   string
		want   string
	}{
		{"actions/checkout@v4", "abc123", "actions/checkout@abc123"},
		{"actions/setup-python@v5", "def456", "actions/setup-python@def456"},
	}
	for _, tt := range tests {
		got := pinned(tt.action, tt.hash)
		if got != tt.want {
			t.Errorf("pinned(%q, %q) = %q, want %q", tt.action, tt.hash, got, tt.want)
		}
	}
}
