package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

const bucketName = "actions"

// ErrLocked is returned by New when the database file is already held by
// another process. Use NewNop() to get a no-op cache that skips all storage.
var ErrLocked = errors.New("cache database locked by another process")

type entry struct {
	Hash     string    `json:"hash"`
	CachedAt time.Time `json:"cached_at"`
}

// Cache is a disk-backed key-value store using BoltDB.
// If db is nil the cache operates in no-op mode: reads always miss and
// writes are silently discarded.
type Cache struct {
	db  *bolt.DB
	ttl time.Duration
}

// New opens (or creates) a BoltDB database at path and returns a Cache.
// ttl is how long entries remain valid; 0 defaults to 24 hours.
// Returns ErrLocked if another process holds the file lock.
func New(path string, ttl time.Duration) (*Cache, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		if errors.Is(err, bolt.ErrTimeout) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return nil, fmt.Errorf("opening bolt db at %s: %w", path, err)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating bucket: %w", err)
	}

	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	return &Cache{db: db, ttl: ttl}, nil
}

// NewNop returns a cache that never stores or returns anything.
// Use this as a fallback when the database file is locked.
func NewNop() *Cache {
	return &Cache{}
}

// Get returns the cached commit hash for the given action key.
// Returns ("", false) on miss, expired entry, or when operating in no-op mode.
func (c *Cache) Get(key string) (string, bool) {
	if c.db == nil {
		return "", false
	}
	var e entry
	err := c.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(bucketName)).Get([]byte(key))
		if v == nil {
			return fmt.Errorf("not found")
		}
		return json.Unmarshal(v, &e)
	})
	if err != nil {
		return "", false
	}
	if time.Since(e.CachedAt) > c.ttl {
		return "", false
	}
	return e.Hash, true
}

// Set stores a commit hash for the given action key.
// Does nothing when operating in no-op mode.
func (c *Cache) Set(key, hash string) error {
	if c.db == nil {
		return nil
	}
	data, err := json.Marshal(entry{Hash: hash, CachedAt: time.Now()})
	if err != nil {
		return err
	}
	return c.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucketName)).Put([]byte(key), data)
	})
}

// Delete removes an entry from the cache.
// Does nothing when operating in no-op mode.
func (c *Cache) Delete(key string) error {
	if c.db == nil {
		return nil
	}
	return c.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucketName)).Delete([]byte(key))
	})
}

// Close closes the underlying BoltDB database.
func (c *Cache) Close() error {
	if c.db == nil {
		return nil
	}
	return c.db.Close()
}
