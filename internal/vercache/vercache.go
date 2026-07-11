// Package vercache persists the output of agent version commands between runs,
// keyed by the resolved binary's identity (path, size, mtime) plus the argv.
// Any update rewrites the binary, changing the key, so a hit is always current;
// a miss simply reruns the command. Version CLIs are often Node startups taking
// 0.3-0.6s each, so this turns repeat `uca -n` / `uca --check` version reads
// into stat calls.
//
// A TTL additionally bounds entries because a few CLIs embed run-time-relative
// text in their version banner (e.g. "released 4h ago"); the version token is
// stable but the banner would drift forever without an expiry.
//
// Set UCA_NO_VERSION_CACHE=1 to disable.
package vercache

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const ttl = 24 * time.Hour

type entry struct {
	Version  string `json:"version"`
	StoredAt int64  `json:"storedAt"` // unix seconds
}

// Cache is a concurrency-safe, lazily-loaded version cache. The zero value of
// *Cache (nil) is a valid no-op cache.
type Cache struct {
	path string

	mu      sync.Mutex
	loaded  bool
	dirty   bool
	entries map[string]entry
}

// Open returns the default cache, or nil (no-op) when disabled via
// UCA_NO_VERSION_CACHE or when no cache location exists.
func Open() *Cache {
	if os.Getenv("UCA_NO_VERSION_CACHE") != "" {
		return nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	return &Cache{path: filepath.Join(dir, "uca", "versions.json")}
}

// key derives the cache key for a version command, or "" when the binary can't
// be identified (no caching). Stat follows symlinks, so a package manager
// rewriting the symlink target invalidates the key.
func key(args []string) string {
	if len(args) == 0 {
		return ""
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d|%s", abs, info.Size(), info.ModTime().UnixNano(), strings.Join(args, "\x00"))
}

// Get returns the cached version for a command, if present and fresh.
func (c *Cache) Get(args []string) (string, bool) {
	if c == nil {
		return "", false
	}
	k := key(args)
	if k == "" {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	e, ok := c.entries[k]
	if !ok || time.Since(time.Unix(e.StoredAt, 0)) > ttl {
		return "", false
	}
	return e.Version, true
}

// Put records a version result. Empty or "unknown" results are not cached, so
// a transient failure never masks a later successful read.
func (c *Cache) Put(args []string, version string) {
	if c == nil || version == "" || version == "unknown" {
		return
	}
	k := key(args)
	if k == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	c.entries[k] = entry{Version: version, StoredAt: time.Now().Unix()}
	c.dirty = true
}

// load reads the cache file once; a missing or corrupt file starts empty.
// Callers must hold c.mu.
func (c *Cache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.entries = map[string]entry{}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var stored map[string]entry
	if json.Unmarshal(data, &stored) != nil {
		return
	}
	c.entries = stored
}

// Save writes the cache atomically (temp file + rename), pruning expired
// entries. A no-op when nothing changed. Concurrent uca processes are
// last-writer-wins, which at worst re-runs a version command next time.
func (c *Cache) Save() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	for k, e := range c.entries {
		if time.Since(time.Unix(e.StoredAt, 0)) > ttl {
			delete(c.entries, k)
		}
	}
	data, err := json.Marshal(c.entries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), "versions-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmp.Name(), c.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	c.dirty = false
	return nil
}
