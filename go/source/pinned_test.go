package source

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-identity/placetest"
)

func TestPinnedCompleteAtCreditsFilesBeneathHeldDirectory(t *testing.T) {
	const name, rel = "local-picture", "weights/component.bin"
	models := NewPinned([]PinnedFile{{Path: rel, Size: 3}}, []PinnedModel{{Name: name, Files: []string{rel}}})
	for _, test := range []struct {
		name   string
		linked bool
		want   []string
	}{
		{name: "plain", want: []string{name}},
		{name: "linked parent", linked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			subfolder := filepath.Join(root, "weights")
			target := subfolder
			if test.linked {
				target = t.TempDir()
				var err error
				if runtime.GOOS == "windows" {
					err = placetest.Junction(subfolder, target)
				} else {
					err = os.Symlink(target, subfolder)
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Remove(subfolder); err != nil {
						t.Error(err)
					}
				})
			} else if err := os.Mkdir(subfolder, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "component.bin"), []byte("abc"), 0600); err != nil {
				t.Fatal(err)
			}
			if got := models.CompleteAt(root); !slices.Equal(got, test.want) {
				t.Fatalf("CompleteAt = %v, want %v", got, test.want)
			}
		})
	}
}
