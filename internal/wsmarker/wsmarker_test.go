package wsmarker

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	a = "29664DF9-67B9-4AAF-9419-B1FD8E3A8994"
	b = "F8290864-F5B5-4B3F-9B93-0B027295DECC"
)

func TestWsmarkerSync(t *testing.T) {
	dir := filepath.Join(t.TempDir(), DirName) // absent: Sync creates it
	if err := Sync(dir, map[string]string{a: "bound", b: "off-topic"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, a)); string(got) != "bound\n" {
		t.Fatalf("a = %q", got)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(dir, b)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	_ = os.WriteFile(filepath.Join(dir, ".keep"), nil, 0o600)
	if err := Sync(dir, map[string]string{b: "bound"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, a)); !os.IsNotExist(err) {
		t.Fatal("orphan marker kept")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, b)); string(got) != "bound\n" {
		t.Fatalf("b = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".keep")); err != nil {
		t.Fatal("dotfile removed")
	}
	if err := Sync(dir, map[string]string{"../evil": "bound"}); err == nil {
		t.Fatal("non-UUID name accepted")
	}
}
