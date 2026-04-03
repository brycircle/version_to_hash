package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		input       string
		wantOwner   string
		wantRepo    string
		wantRef     string
		wantErrPart string
	}{
		{"actions/checkout@v4", "actions", "checkout", "v4", ""},
		{"actions/setup-python@v5", "actions", "setup-python", "v5", ""},
		{"actions/checkout@abc123def456abc123def456abc123def456abc1", "actions", "checkout", "abc123def456abc123def456abc123def456abc1", ""},
		{"actions/checkout", "", "", "", "missing '@'"},
		{"noslash@v4", "", "", "", "expected owner/repo"},
		{"@v4", "", "", "", "expected owner/repo"},
		{"owner/repo@", "", "", "", "non-empty"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			owner, repo, ref, err := ParseRef(tt.input)
			if tt.wantErrPart != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrPart)
				}
				if !containsStr(err.Error(), tt.wantErrPart) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tt.wantOwner || repo != tt.wantRepo || ref != tt.wantRef {
				t.Fatalf("got (%q, %q, %q), want (%q, %q, %q)",
					owner, repo, ref, tt.wantOwner, tt.wantRepo, tt.wantRef)
			}
		})
	}
}

func TestIsFullHash(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"abc123def456abc123def456abc123def456abc1", true},
		{"ABC123DEF456ABC123DEF456ABC123DEF456ABC1", true},
		{"abc123", false},                               // too short
		{"abc123def456abc123def456abc123def456abc1x", false}, // too long
		{"abc123def456abc123def456abc123def456abcg", false},  // invalid char
		{"v4", false},
	}
	for _, tt := range tests {
		if got := isFullHash(tt.s); got != tt.want {
			t.Errorf("isFullHash(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestResolveToHash_AlreadyHash(t *testing.T) {
	c := NewClient("")
	const hash = "abc123def456abc123def456abc123def456abc1"
	got, err := c.ResolveToHash("actions/checkout@" + hash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != hash {
		t.Fatalf("got %q, want %q", got, hash)
	}
}

func TestResolveToHash_LightweightTag(t *testing.T) {
	const commitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/actions/checkout/git/ref/tags/v4" {
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{"type": "commit", "sha": commitSHA},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: srv.URL}
	got, err := c.ResolveToHash("actions/checkout@v4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != commitSHA {
		t.Fatalf("got %q, want %q", got, commitSHA)
	}
}

func TestResolveToHash_AnnotatedTag(t *testing.T) {
	const tagSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const commitSHA = "cccccccccccccccccccccccccccccccccccccccc"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/actions/checkout/git/ref/tags/v4":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{"type": "tag", "sha": tagSHA},
			})
		case "/repos/actions/checkout/git/tags/" + tagSHA:
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{"type": "commit", "sha": commitSHA},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: srv.URL}
	got, err := c.ResolveToHash("actions/checkout@v4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != commitSHA {
		t.Fatalf("got %q, want %q", got, commitSHA)
	}
}

func TestResolveToHash_FallsBackToBranch(t *testing.T) {
	const commitSHA = "dddddddddddddddddddddddddddddddddddddddd"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/actions/checkout/git/ref/tags/main":
			http.NotFound(w, r)
		case "/repos/actions/checkout/git/ref/heads/main":
			json.NewEncoder(w).Encode(map[string]any{
				"object": map[string]any{"type": "commit", "sha": commitSHA},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: srv.URL}
	got, err := c.ResolveToHash("actions/checkout@main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != commitSHA {
		t.Fatalf("got %q, want %q", got, commitSHA)
	}
}

func TestResolveToHash_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &Client{http: srv.Client(), baseURL: srv.URL}
	_, err := c.ResolveToHash("actions/checkout@nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent ref, got nil")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
