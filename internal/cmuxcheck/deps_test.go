package cmuxcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDepsTranscriptFound(t *testing.T) {
	cfg := t.TempDir()
	dir := filepath.Join(cfg, "projects", "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sid-x.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := DefaultDeps()
	if ok, err := d.TranscriptFound(cfg, "sid-x"); !ok || err != nil {
		t.Fatalf("sid-x = %v, %v; want true, nil", ok, err)
	}
	if ok, err := d.TranscriptFound(cfg, "sid-y"); ok || err != nil {
		t.Fatalf("sid-y = %v, %v; want false, nil", ok, err)
	}
	// A glob metacharacter in the id is a lookup failure, never "absent"
	// (filepath.Glob reports ErrBadPattern only against an existing entry).
	if ok, err := d.TranscriptFound(cfg, "a["); ok || err == nil {
		t.Fatalf("a[ = %v, %v; want false and an error", ok, err)
	}
}
