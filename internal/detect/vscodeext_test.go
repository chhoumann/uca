package detect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadCodeExtensions(t *testing.T) {
	dir := t.TempDir()
	manifest := `[
	 {"identifier":{"id":"pub.old"},"version":"1.0.0","relativeLocation":"pub.old-1.0.0"},
	 {"identifier":{"id":"pub.old"},"version":"2.0.0","relativeLocation":"pub.old-2.0.0"},
	 {"identifier":{"id":"pub.gone"},"version":"3.0.0","relativeLocation":"pub.gone-3.0.0"},
	 {"identifier":{"id":"pub.keep"},"version":"0.5.0","relativeLocation":"pub.keep-0.5.0"}
	]`
	if err := os.WriteFile(filepath.Join(dir, "extensions.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".obsolete"), []byte(`{"pub.gone-3.0.0":true,"pub.old-1.0.0":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readCodeExtensions(dir)
	want := map[string]string{"pub.old": "2.0.0", "pub.keep": "0.5.0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readCodeExtensions = %v, want %v", got, want)
	}

	if readCodeExtensions("") != nil {
		t.Fatal("empty dir must return nil")
	}
	if readCodeExtensions(t.TempDir()) != nil {
		t.Fatal("missing manifest must return nil (fall back to CLI)")
	}
}
