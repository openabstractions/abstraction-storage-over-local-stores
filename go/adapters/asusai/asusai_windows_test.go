//go:build windows

package asusai

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
)

// A file this account owns is never opened by the real rule (identity
// ID-D2 through trustedpath.CheckFile): the listing reports it unread and
// the record never names it. The rule's own descriptor tests live with it
// in abstraction-identity's go/trustedpath. An elevated test run creates
// files owned by Administrators, which the rule trusts, so the test runs
// only unelevated.
func TestAFileOfThisAccountIsNeverOpened(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("elevated: new files are owned by Administrators")
	}
	_, env, record := machine(t)
	trust = trustedFile
	if err := trustedFile(full(env, transformerRel)); err == nil || !strings.Contains(err.Error(), "owned by") {
		t.Fatalf("trustedFile on this account's file: %v", err)
	}
	o := settled(t, env, "asus-aicreator")
	if len(o.Manifests) != 0 || len(o.Objects) != 0 {
		t.Fatalf("listed %v", fixture.Locators(o))
	}
	for _, e := range o.Errors {
		if e.Locator != within(vaePthRel) && !strings.Contains(e.Detail, "not opened") {
			t.Fatalf("error %+v", e)
		}
	}
	if _, err := os.Stat(record); err == nil {
		t.Fatalf("a digest was recorded: %v", hashed(t, env, record))
	}
	if _, err := os.Stat(filepath.Dir(record)); err == nil {
		t.Fatal("the record folder was made though nothing was hashed")
	}
}
