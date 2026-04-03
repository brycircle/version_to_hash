package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client queries the GitHub API to resolve action refs to commit hashes.
type Client struct {
	http    *http.Client
	token   string
	baseURL string
}

// NewClient creates a GitHub API client. token is optional but recommended
// to avoid rate limiting (60 req/hour unauthenticated vs 5000 authenticated).
func NewClient(token string) *Client {
	return &Client{
		http:    &http.Client{Timeout: 15 * time.Second},
		token:   token,
		baseURL: "https://api.github.com",
	}
}

// NewClientWithBaseURL creates a client that targets a custom base URL.
// Intended for testing with httptest servers.
func NewClientWithBaseURL(token, baseURL string) *Client {
	return &Client{
		http:    &http.Client{Timeout: 15 * time.Second},
		token:   token,
		baseURL: baseURL,
	}
}

// ParseOwnerRepo parses "owner/repo" or "owner/repo@ref" into owner and repo.
// Any version suffix is stripped and ignored.
func ParseOwnerRepo(action string) (owner, repo string, err error) {
	s := action
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[:i]
	}
	slash := strings.Index(s, "/")
	if slash < 0 {
		return "", "", fmt.Errorf("invalid action %q: expected owner/repo", action)
	}
	owner, repo = s[:slash], s[slash+1:]
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("invalid action %q: owner and repo must be non-empty", action)
	}
	return owner, repo, nil
}

// ParseRef parses "owner/repo@ref" into its components.
func ParseRef(action string) (owner, repo, ref string, err error) {
	at := strings.Index(action, "@")
	if at < 0 {
		return "", "", "", fmt.Errorf("invalid action %q: missing '@'", action)
	}
	ref = action[at+1:]
	repoPath := action[:at]

	slash := strings.Index(repoPath, "/")
	if slash < 0 {
		return "", "", "", fmt.Errorf("invalid action %q: expected owner/repo@ref", action)
	}
	owner = repoPath[:slash]
	repo = repoPath[slash+1:]

	if owner == "" || repo == "" || ref == "" {
		return "", "", "", fmt.Errorf("invalid action %q: owner, repo, and ref must be non-empty", action)
	}
	return owner, repo, ref, nil
}

// ResolveToHash returns the full commit SHA for the given action reference.
// If the ref is already a 40-character hex commit hash it is returned as-is.
func (c *Client) ResolveToHash(action string) (string, error) {
	owner, repo, ref, err := ParseRef(action)
	if err != nil {
		return "", err
	}

	if isFullHash(ref) {
		return ref, nil
	}

	// Try tag first, then branch.
	hash, err := c.resolveRef(owner, repo, "tags/"+ref)
	if err == nil {
		return hash, nil
	}

	hash, err = c.resolveRef(owner, repo, "heads/"+ref)
	if err == nil {
		return hash, nil
	}

	return "", fmt.Errorf("could not resolve %q for %s/%s: not found as tag or branch", ref, owner, repo)
}

type gitRefResponse struct {
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

type gitTagResponse struct {
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

func (c *Client) resolveRef(owner, repo, refPath string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/git/ref/%s", c.baseURL, owner, repo, refPath)

	resp, err := c.doGet(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("ref %s not found", refPath)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API %s: HTTP %d", url, resp.StatusCode)
	}

	var r gitRefResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("decoding ref response: %w", err)
	}

	switch r.Object.Type {
	case "commit":
		return r.Object.SHA, nil
	case "tag":
		return c.dereferenceAnnotatedTag(owner, repo, r.Object.SHA)
	default:
		return "", fmt.Errorf("unexpected object type %q", r.Object.Type)
	}
}

// dereferenceAnnotatedTag follows an annotated tag object to its commit SHA.
func (c *Client) dereferenceAnnotatedTag(owner, repo, tagSHA string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/git/tags/%s", c.baseURL, owner, repo, tagSHA)

	resp, err := c.doGet(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API %s: HTTP %d", url, resp.StatusCode)
	}

	var t gitTagResponse
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", fmt.Errorf("decoding tag response: %w", err)
	}

	if t.Object.Type != "commit" {
		return "", fmt.Errorf("annotated tag does not point to a commit (type=%q)", t.Object.Type)
	}
	return t.Object.SHA, nil
}

func (c *Client) doGet(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "version-to-hash/1.0")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return c.http.Do(req)
}

// LatestRelease returns the tag name of the latest published release for
// owner/repo. Pre-releases are excluded (GitHub's /releases/latest semantics).
func (c *Client) LatestRelease(owner, repo string) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.baseURL, owner, repo)

	resp, err := c.doGet(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no releases found for %s/%s", owner, repo)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API %s: HTTP %d", url, resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding release response: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("latest release for %s/%s has no tag", owner, repo)
	}
	return release.TagName, nil
}

// isFullHash reports whether s is a valid 40-character hex commit hash.
func isFullHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, ch := range s {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
			return false
		}
	}
	return true
}
