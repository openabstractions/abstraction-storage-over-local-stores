package fixture

import (
	"path/filepath"
	"testing"
)

func TestSamePathPreservesExpectedSpelling(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "Models", "weights")
	if SamePath(want, filepath.Join(root, "models", "weights")) {
		t.Fatal("locator assertion accepted a different directory spelling")
	}
	if !SamePath(want, filepath.Join(root, "Models")+string(filepath.Separator)+"."+string(filepath.Separator)+"weights") {
		t.Fatal("locator assertion rejected equivalent cleaned spelling")
	}
}
