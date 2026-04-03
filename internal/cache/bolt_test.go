package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestCache(t *testing.T, ttl time.Duration) *Cache {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	c, err := New(path, ttl)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestGetMiss(t *testing.T) {
	c := newTestCache(t, time.Hour)
	if _, ok := c.Get("actions/checkout@v4"); ok {
		t.Fatal("expected miss on empty cache")
	}
}

func TestSetAndGet(t *testing.T) {
	c := newTestCache(t, time.Hour)
	const key = "actions/checkout@v4"
	const hash = "abc123def456abc123def456abc123def456abc1"

	if err := c.Set(key, hash); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if got != hash {
		t.Fatalf("got hash %q, want %q", got, hash)
	}
}

func TestExpiredEntry(t *testing.T) {
	c := newTestCache(t, 50*time.Millisecond)
	const key = "actions/setup-node@v4"
	const hash = "abc123def456abc123def456abc123def456abc1"

	if err := c.Set(key, hash); err != nil {
		t.Fatalf("Set: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if _, ok := c.Get(key); ok {
		t.Fatal("expected miss after TTL expiry")
	}
}

func TestDelete(t *testing.T) {
	c := newTestCache(t, time.Hour)
	const key = "actions/checkout@v4"

	if err := c.Set(key, "somehash"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := c.Get(key); ok {
		t.Fatal("expected miss after Delete")
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.db")
	const key = "actions/checkout@v4"
	const hash = "abc123def456abc123def456abc123def456abc1"

	c1, err := New(path, time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c1.Set(key, hash); err != nil {
		t.Fatalf("Set: %v", err)
	}
	c1.Close()

	c2, err := New(path, time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c2.Close()

	got, ok := c2.Get(key)
	if !ok {
		t.Fatal("expected hit in reopened cache")
	}
	if got != hash {
		t.Fatalf("got %q, want %q", got, hash)
	}
}

func TestNewMissingDir(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "nonexistent", "cache.db"), time.Hour)
	if err == nil {
		t.Fatal("expected error for missing parent directory")
	}
	// clean up in case it somehow succeeded
	os.Remove(filepath.Join(t.TempDir(), "nonexistent", "cache.db"))
}
