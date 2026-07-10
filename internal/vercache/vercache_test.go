package vercache

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// testBinary creates an executable file on PATH so key() can resolve it.
func testBinary(t *testing.T, name string) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PATH fixture is POSIX-only")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return []string{name, "--version"}
}

func testCache(t *testing.T) *Cache {
	t.Helper()
	return &Cache{path: filepath.Join(t.TempDir(), "versions.json")}
}

func TestGetPutRoundTrip(t *testing.T) {
	args := testBinary(t, "tool")
	c := testCache(t)
	if _, ok := c.Get(args); ok {
		t.Fatal("empty cache must miss")
	}
	c.Put(args, "1.2.3")
	got, ok := c.Get(args)
	if !ok || got != "1.2.3" {
		t.Fatalf("Get = %q,%v after Put", got, ok)
	}
	// Different argv is a different key.
	if _, ok := c.Get([]string{"tool", "-V"}); ok {
		t.Fatal("different args must miss")
	}
}

func TestBinaryChangeInvalidates(t *testing.T) {
	args := testBinary(t, "tool")
	c := testCache(t)
	c.Put(args, "1.2.3")
	// Simulate an update: rewrite the binary (size and mtime change).
	path, _ := filepath.Abs(filepath.Join(os.Getenv("PATH"), "tool"))
	if err := os.WriteFile(path, []byte("#!/bin/sh\n# v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(args); ok {
		t.Fatal("rewritten binary must invalidate the cached version")
	}
}

func TestUnknownAndEmptyNotCached(t *testing.T) {
	args := testBinary(t, "tool")
	c := testCache(t)
	c.Put(args, "unknown")
	c.Put(args, "")
	if _, ok := c.Get(args); ok {
		t.Fatal("failure results must not be cached")
	}
}

func TestSaveAndReload(t *testing.T) {
	args := testBinary(t, "tool")
	c := testCache(t)
	c.Put(args, "9.9.9")
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded := &Cache{path: c.path}
	got, ok := reloaded.Get(args)
	if !ok || got != "9.9.9" {
		t.Fatalf("reloaded Get = %q,%v", got, ok)
	}
}

func TestExpiredEntryMisses(t *testing.T) {
	args := testBinary(t, "tool")
	c := testCache(t)
	c.Put(args, "1.0.0")
	for k, e := range c.entries {
		e.StoredAt = time.Now().Add(-2 * ttl).Unix()
		c.entries[k] = e
	}
	if _, ok := c.Get(args); ok {
		t.Fatal("expired entry must miss")
	}
}

func TestNilCacheIsNoop(t *testing.T) {
	var c *Cache
	if _, ok := c.Get([]string{"x"}); ok {
		t.Fatal("nil cache must miss")
	}
	c.Put([]string{"x"}, "1")
	if err := c.Save(); err != nil {
		t.Fatalf("nil Save: %v", err)
	}
}

func TestCorruptFileStartsEmpty(t *testing.T) {
	args := testBinary(t, "tool")
	c := testCache(t)
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(args); ok {
		t.Fatal("corrupt cache must miss, not error")
	}
	c.Put(args, "1.0.0")
	if err := c.Save(); err != nil {
		t.Fatalf("Save over corrupt file: %v", err)
	}
}

func TestOpenDisabled(t *testing.T) {
	t.Setenv("UCA_NO_VERSION_CACHE", "1")
	if Open() != nil {
		t.Fatal("UCA_NO_VERSION_CACHE must disable the cache")
	}
}
