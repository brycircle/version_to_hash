package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/version-to-hash/version-to-hash/internal/api"
	"github.com/version-to-hash/version-to-hash/internal/cache"
	"github.com/version-to-hash/version-to-hash/internal/github"
	internalmcp "github.com/version-to-hash/version-to-hash/internal/mcp"
)

func main() {
	stdio := flag.Bool("stdio", false, "Run as an MCP stdio server instead of an HTTP server")
	flag.Parse()

	log := newLogger(*stdio)

	// ── Cache ────────────────────────────────────────────────────────────────
	// Default to ~/.config/version-to-hash/bolt.db so the binary works
	// out of the box without any setup. The Dockerfile sets CACHE_PATH=/data/cache.db
	// via ENV, so Docker behaviour is unchanged.
	defaultCache := filepath.Join(userConfigDir(), "version-to-hash", "bolt.db")
	cachePath := getEnv("CACHE_PATH", defaultCache)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0700); err != nil {
		log.WithError(err).Fatal("failed to create cache directory")
	}
	cacheTTL := parseDurationHours(getEnv("CACHE_TTL_HOURS", "24"))

	c, err := cache.New(cachePath, cacheTTL)
	if err != nil {
		if errors.Is(err, cache.ErrLocked) {
			log.Warn("cache is locked by another instance – running without cache")
			c = cache.NewNop()
		} else {
			log.WithError(err).Fatal("failed to open cache")
		}
	}
	defer c.Close()

	// ── GitHub client ────────────────────────────────────────────────────────
	ghClient := github.NewClient(os.Getenv("GITHUB_TOKEN"))

	// ── Stdio MCP mode ───────────────────────────────────────────────────────
	// When launched with --stdio, skip HTTP entirely and serve MCP over
	// stdin/stdout so Claude Code (and other MCP clients) can run the binary
	// as a subprocess.
	if *stdio {
		log.WithField("cache_path", cachePath).Info("starting MCP stdio server")
		if err := internalmcp.NewServer(c, ghClient, log).ServeStdio(); err != nil {
			log.WithError(err).Fatal("stdio server error")
		}
		return
	}

	// ── REST API (Gin) ───────────────────────────────────────────────────────
	if getEnv("GIN_MODE", "") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.Use(gin.Recovery(), requestLogger(log))
	api.NewHandler(c, ghClient, log).RegisterRoutes(router)

	// ── MCP server ───────────────────────────────────────────────────────────
	mcpSrv := internalmcp.NewServer(c, ghClient, log)
	router.Any("/mcp", gin.WrapH(mcpSrv.HTTPHandler()))
	router.Any("/mcp/*path", gin.WrapH(mcpSrv.HTTPHandler()))

	// ── Start ────────────────────────────────────────────────────────────────
	addr := ":" + getEnv("PORT", "8080")
	log.WithFields(logrus.Fields{
		"addr":       addr,
		"cache_path": cachePath,
		"cache_ttl":  cacheTTL,
	}).Info("starting version-to-hash server")

	if err := http.ListenAndServe(addr, router); err != nil {
		log.WithError(err).Fatal("server stopped")
	}
}

func newLogger(stdio bool) *logrus.Logger {
	log := logrus.New()
	log.SetFormatter(&logrus.JSONFormatter{})
	// In stdio mode stdout is the MCP transport; logs must go to stderr.
	if stdio {
		log.SetOutput(os.Stderr)
	}
	lvl, err := logrus.ParseLevel(getEnv("LOG_LEVEL", "info"))
	if err != nil {
		lvl = logrus.InfoLevel
	}
	log.SetLevel(lvl)
	return log
}

func userConfigDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config")
	}
	return os.TempDir()
}

func requestLogger(log *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		log.WithFields(logrus.Fields{
			"method": c.Request.Method,
			"path":   c.Request.URL.Path,
			"status": c.Writer.Status(),
		}).Info("request")
	}
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func parseDurationHours(s string) time.Duration {
	h, err := strconv.Atoi(s)
	if err != nil || h <= 0 {
		fmt.Fprintf(os.Stderr, "invalid CACHE_TTL_HOURS %q, defaulting to 24\n", s)
		h = 24
	}
	return time.Duration(h) * time.Hour
}
