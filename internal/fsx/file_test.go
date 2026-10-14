package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicKeepsExistingDirectoryMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "claim-url")
	if err := WriteFileAtomic(path, []byte("x\n"), 0o600, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("existing directory mode = %o, want 755", info.Mode().Perm())
	}
	assertFileMode(t, path, 0o600)

	created := filepath.Join(t.TempDir(), "new", "auth.json")
	if err := WriteFileAtomic(created, []byte("{}\n"), 0o600, false); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(filepath.Dir(created))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("created directory mode = %o, want 700", info.Mode().Perm())
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}
