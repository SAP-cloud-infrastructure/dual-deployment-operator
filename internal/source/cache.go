package source

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	// DefaultCacheSize is the default maximum cache size (500 MiB).
	DefaultCacheSize = 500 * 1024 * 1024
)

// SourceCache is the core caching abstraction for rendered sources.
type SourceCache interface {
	// Get retrieves a cached source by key.
	// Returns the path to the cached content (may be a directory or file).
	// If cache miss, returns ("", nil) (no error on miss).
	Get(ctx context.Context, key string) (string, error)

	// Put stores a source at the given key.
	// Overwrites existing entry for that key.
	Put(ctx context.Context, key string, srcPath string) error

	// Keys returns all cached keys (for debugging, eviction decisions).
	Keys(ctx context.Context) ([]string, error)

	// Evict removes a key from the cache.
	// Safe to call on non-existent key (idempotent).
	Evict(ctx context.Context, key string) error

	// Close releases resources (e.g., cleanup temp dirs).
	Close() error
}

// cacheEntry holds metadata for a cached source.
type cacheEntry struct {
	path       string
	size       int64
	lastAccess time.Time
}

// LRUCache is a thread-safe LRU cache implementation bounded by total size.
type LRUCache struct {
	mu        sync.RWMutex
	entries   map[string]*cacheEntry
	totalSize int64
	maxSize   int64
	basePath  string
}

// NewLRUCache creates a new LRU cache.
// basePath: directory to store cached entries. If empty, uses os.TempDir().
// maxSize: maximum total cache size in bytes. If <= 0, uses DefaultCacheSize.
func NewLRUCache(basePath string, maxSize int64) (*LRUCache, error) {
	if basePath == "" {
		basePath = os.TempDir()
	}
	if maxSize <= 0 {
		maxSize = DefaultCacheSize
	}

	// Ensure basePath exists
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory %q: %w", basePath, err)
	}

	return &LRUCache{
		entries:   make(map[string]*cacheEntry),
		totalSize: 0,
		maxSize:   maxSize,
		basePath:  basePath,
	}, nil
}

// Get retrieves a cached source by key.
// Updates lastAccess time on hit.
// Returns ("", nil) on miss.
func (c *LRUCache) Get(ctx context.Context, key string) (string, error) {
	// Check context cancellation
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return "", nil // cache miss
	}

	// Update lastAccess (LRU tracking)
	entry.lastAccess = time.Now()
	return entry.path, nil
}

// Put stores a source at the given key.
// Calculates entry size, evicts LRU entries if totalSize > maxSize.
func (c *LRUCache) Put(ctx context.Context, key string, srcPath string) error {
	// Check context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Calculate size of srcPath
	size, err := c.dirSize(srcPath)
	if err != nil {
		return fmt.Errorf("failed to calculate size of %q: %w", srcPath, err)
	}

	// If key already exists, subtract its size first
	if existing, ok := c.entries[key]; ok {
		c.totalSize -= existing.size
		// Remove old path from disk
		if err := os.RemoveAll(existing.path); err != nil {
			// Log but don't fail
			fmt.Printf("warning: failed to remove old cache entry %q: %v\n", existing.path, err)
		}
	}

	// Create new entry
	newEntry := &cacheEntry{
		path:       srcPath,
		size:       size,
		lastAccess: time.Now(),
	}
	c.entries[key] = newEntry
	c.totalSize += size

	// Evict LRU entries if over limit
	for c.totalSize > c.maxSize && len(c.entries) > 0 {
		c.evictLRU()
	}

	return nil
}

// Keys returns all cached keys in sorted order.
func (c *LRUCache) Keys(ctx context.Context) ([]string, error) {
	// Check context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// Evict removes a key from the cache.
// Idempotent: safe to call on non-existent key.
func (c *LRUCache) Evict(ctx context.Context, key string) error {
	// Check context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil // idempotent: no error on non-existent key
	}

	// Remove from map
	delete(c.entries, key)

	// Decrement totalSize
	c.totalSize -= entry.size

	// Remove directory/file from disk
	if err := os.RemoveAll(entry.path); err != nil {
		return fmt.Errorf("failed to remove cache entry %q: %w", entry.path, err)
	}

	return nil
}

// Close releases all cache resources.
// Walks basePath and removes all entries from disk.
func (c *LRUCache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove all entries from disk
	for key, entry := range c.entries {
		if err := os.RemoveAll(entry.path); err != nil {
			fmt.Printf("warning: failed to remove cache entry %q during Close: %v\n", entry.path, err)
		}
		delete(c.entries, key)
	}

	c.totalSize = 0
	return nil
}

// evictLRU removes the least-recently-used entry (must hold lock).
func (c *LRUCache) evictLRU() {
	var lruKey string
	var lruTime time.Time

	// Find entry with earliest lastAccess
	for k, entry := range c.entries {
		if lruTime.IsZero() || entry.lastAccess.Before(lruTime) {
			lruKey = k
			lruTime = entry.lastAccess
		}
	}

	if lruKey != "" {
		entry := c.entries[lruKey]
		delete(c.entries, lruKey)
		c.totalSize -= entry.size

		// Remove from disk
		if err := os.RemoveAll(entry.path); err != nil {
			fmt.Printf("warning: failed to remove LRU evicted entry %q: %v\n", entry.path, err)
		}
	}
}

// dirSize recursively calculates the size of a directory or file.
func (c *LRUCache) dirSize(path string) (int64, error) {
	// Check if path is a file first (faster path)
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("failed to stat %q: %w", path, err)
	}

	if !info.IsDir() {
		return info.Size(), nil
	}

	// Walk directory tree
	var totalSize int64
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			totalSize += info.Size()
		}

		return nil
	})

	return totalSize, err
}
