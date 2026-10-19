package indexyaml

import (
	"os"
	"strings"
	"testing"
)

// The local-stores module is core OA source and carries no third-party YAML
// library (VISION.md:2220, backlog K-9).
func TestModuleHasNoYAMLDependency(t *testing.T) {
	for _, f := range []string{"../../go.mod", "../../go.sum"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "yaml") {
			t.Errorf("%s names a yaml module", f)
		}
	}
}
