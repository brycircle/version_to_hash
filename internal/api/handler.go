package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/version-to-hash/version-to-hash/internal/cache"
	"github.com/version-to-hash/version-to-hash/internal/github"
)

// Handler holds dependencies for the REST API.
type Handler struct {
	cache    *cache.Cache
	ghClient *github.Client
	log      *logrus.Logger
}

// NewHandler creates a new Handler.
func NewHandler(c *cache.Cache, ghClient *github.Client, log *logrus.Logger) *Handler {
	return &Handler{cache: c, ghClient: ghClient, log: log}
}

// RegisterRoutes attaches the API routes to the given Gin engine.
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	r.GET("/health", h.health)
	r.GET("/resolve", h.resolveGET)
	r.POST("/resolve", h.resolvePOST)
}

func (h *Handler) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// resolveResponse is returned by both GET and POST /resolve.
type resolveResponse struct {
	Action   string `json:"action"`
	Resolved string `json:"resolved"`
	Hash     string `json:"hash"`
	Cached   bool   `json:"cached"`
}

// GET /resolve?action=actions/checkout@v4
func (h *Handler) resolveGET(c *gin.Context) {
	action := c.Query("action")
	if action == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query parameter 'action' is required"})
		return
	}
	h.handleResolve(c, action)
}

type resolveRequest struct {
	Action string `json:"action" binding:"required"`
}

// POST /resolve  body: {"action": "actions/checkout@v4"}
func (h *Handler) resolvePOST(c *gin.Context) {
	var req resolveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.handleResolve(c, req.Action)
}

func (h *Handler) handleResolve(c *gin.Context, action string) {
	hash, cached, err := h.resolve(action)
	if err != nil {
		h.log.WithError(err).WithField("action", action).Error("resolve failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resolveResponse{
		Action:   action,
		Resolved: pinned(action, hash),
		Hash:     hash,
		Cached:   cached,
	})
}

// resolve returns the commit hash for action, using the cache when possible.
func (h *Handler) resolve(action string) (hash string, cached bool, err error) {
	if cached, ok := h.cache.Get(action); ok {
		h.log.WithField("action", action).Debug("cache hit")
		return cached, true, nil
	}

	h.log.WithField("action", action).Info("cache miss – querying GitHub API")
	hash, err = h.ghClient.ResolveToHash(action)
	if err != nil {
		return "", false, fmt.Errorf("resolving %s: %w", action, err)
	}

	if storeErr := h.cache.Set(action, hash); storeErr != nil {
		h.log.WithError(storeErr).Warn("failed to cache result")
	}
	return hash, false, nil
}

// pinned replaces the version tag in an action reference with a commit hash.
// "actions/checkout@v4", "abc123..." → "actions/checkout@abc123..."
func pinned(action, hash string) string {
	if i := strings.Index(action, "@"); i >= 0 {
		return action[:i+1] + hash
	}
	return action + "@" + hash
}
