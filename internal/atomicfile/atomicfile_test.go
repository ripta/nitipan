package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesTheFileAndLeavesNoTempFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")

	for _, content := range []string{"one", "two"} {
		if err := Write(dir, "f.json", []byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, "f.json"))
	if err != nil || string(got) != "two" {
		t.Fatalf("read: %q, %v", got, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v, %v", entries, err)
	}
}
