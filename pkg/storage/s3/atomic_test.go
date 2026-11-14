package s3

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicSwapDir(t *testing.T) {
	tempDir := t.TempDir()

	stagingDir := filepath.Join(tempDir, "staging")
	if err := os.MkdirAll(stagingDir, 0750); err != nil {
		t.Fatalf("failed to create staging dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "repomd.xml"), []byte("<repomd>v1</repomd>"), 0600); err != nil {
		t.Fatalf("failed to write staging file: %v", err)
	}

	finalDir := filepath.Join(tempDir, "repodata")

	// First swap: finalDir does not exist yet
	if err := atomicSwapDir(stagingDir, finalDir); err != nil {
		t.Fatalf("atomicSwapDir failed on initial swap: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(finalDir, "repomd.xml"))
	if err != nil || string(content) != "<repomd>v1</repomd>" {
		t.Fatalf("unexpected content after initial swap: %s, err: %v", string(content), err)
	}

	// Second swap: finalDir already exists and is replaced atomically
	stagingDir2 := filepath.Join(tempDir, "staging2")
	if err := os.MkdirAll(stagingDir2, 0750); err != nil {
		t.Fatalf("failed to create staging2 dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir2, "repomd.xml"), []byte("<repomd>v2</repomd>"), 0600); err != nil {
		t.Fatalf("failed to write staging2 file: %v", err)
	}
	if err := atomicSwapDir(stagingDir2, finalDir); err != nil {
		t.Fatalf("atomicSwapDir failed on second swap: %v", err)
	}
	content2, err := os.ReadFile(filepath.Join(finalDir, "repomd.xml"))
	if err != nil || string(content2) != "<repomd>v2</repomd>" {
		t.Fatalf("unexpected content after second swap: %s, err: %v", string(content2), err)
	}
}
