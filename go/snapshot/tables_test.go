package snapshot_test

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"

	"github.com/openabstractions/abstraction-storage-over-local-stores/go/internal/fixture"
	"github.com/openabstractions/abstraction-storage-over-local-stores/go/snapshot"
)

// The whole fixture machine, as the five tables of §5.4, byte-compared with
// testdata/expected. The home directory is spelled {{ROOT}} and separators are
// slashes, so the tables are the same on every checkout. Modification times
// are left out: a checkout sets them. Rewrite the tables with
// INVENTORYD_UPDATE_TABLES=1 and review the diff.
func TestTablesMatchCorpus(t *testing.T) {
	env := machine(t)
	r := snapshot.Take(env, snapshot.Adapters())
	tables := Tables(r.Page, env.Home)
	dir := filepath.Join(fixture.Testdata(), "expected")
	update := os.Getenv("INVENTORYD_UPDATE_TABLES") == "1"
	for _, name := range []string{"stores.tsv", "manifests.tsv", "objects.tsv", "holds.tsv", "dangling.tsv"} {
		got := tables[name]
		path := filepath.Join(dir, name)
		if update {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (INVENTORYD_UPDATE_TABLES=1 writes it)", name, err)
		}
		if got != string(want) {
			t.Errorf("%s differs from testdata/expected:\n%s", name, firstDifference(string(want), got))
		}
	}
}

// Tables renders a source page as the §5.4 tables, one row per record, LF.
func Tables(page content.SourcePage, home string) map[string]string {
	norm := func(s string) string {
		s = strings.ReplaceAll(s, home, "{{ROOT}}")
		s = strings.ReplaceAll(s, filepath.ToSlash(home), "{{ROOT}}")
		return strings.ReplaceAll(s, `\`, "/")
	}
	join := func(rows []string, header string) string {
		sort.Strings(rows)
		return header + "\n" + strings.Join(append(rows, ""), "\n")
	}
	names := map[string]string{}
	var stores, manifests, objects, holds, dangling []string
	for _, s := range page.Stores {
		var errs []string
		for _, e := range s.Errors {
			errs = append(errs, string(e.Kind)+" "+norm(e.Locator))
		}
		sort.Strings(errs)
		stores = append(stores, strings.Join([]string{s.Name, s.Program, norm(s.Locator), norm(s.Rule), strconv.FormatBool(s.Present), s.Placement.String(), s.Domain, strings.Join(errs, "; ")}, "\t"))
	}
	for _, m := range page.Manifests {
		var ns, es, ds []string
		for _, n := range m.Manifest.Names {
			ns = append(ns, n.Scheme+":"+n.Name)
		}
		if len(ns) > 0 {
			names[m.Manifest.ID] = ns[0]
		}
		for _, e := range m.Manifest.Entries {
			es = append(es, strings.Join([]string{e.Role, strconv.FormatInt(e.Size, 10), e.MediaType, e.Evidence.String(), e.Digest, e.Locator}, " "))
		}
		for k, v := range m.Manifest.Descriptor {
			ds = append(ds, k+"="+v)
		}
		sort.Strings(ds)
		manifests = append(manifests, strings.Join([]string{m.Manifest.Store, strings.Join(ns, "; "), m.Manifest.ID, m.Manifest.Addressing.String(), m.Manifest.Kind, strings.Join(es, "; "), strings.Join(ds, " ")}, "\t"))
		for _, h := range m.Holds {
			b := content.Basis{}
			if h.Basis != nil {
				b = *h.Basis
			}
			holds = append(holds, strings.Join([]string{m.Manifest.Store, ns[0], h.Holder.Program, h.Holder.Instance, h.Purpose, h.Lifetime.String(), string(h.Attestation), norm(b.Index), b.Key, b.Role}, "\t"))
		}
	}
	for _, o := range page.Objects {
		objects = append(objects, strings.Join([]string{o.Store, o.Object.Locator, strconv.FormatInt(o.Object.Size, 10), strconv.FormatBool(o.Object.Partial), o.Object.Evidence.String(), o.Object.Digest}, "\t"))
	}
	for _, d := range page.Dangling {
		dangling = append(dangling, strings.Join([]string{d.Holder.Program, d.Holder.Instance, norm(d.Basis.Index), d.Basis.Key, d.Basis.Role, norm(d.Reference), d.ExpectedDigest, strconv.FormatInt(d.ExpectedSize, 10)}, "\t"))
	}
	return map[string]string{
		"stores.tsv":    join(stores, "name\tprogram\tlocator\trule\tpresent\tplacement\tdomain\terrors"),
		"manifests.tsv": join(manifests, "store\tnames\tid\taddressing\tkind\tentries (role size media_type evidence digest locator)\tdescriptor"),
		"objects.tsv":   join(objects, "store\tlocator\tsize\tpartial\tevidence\tdigest"),
		"holds.tsv":     join(holds, "store\tmanifest\tprogram\tinstance\tpurpose\tlifetime\tattestation\tbasis index\tbasis key\tbasis role"),
		"dangling.tsv":  join(dangling, "program\tinstance\tbasis index\tbasis key\tbasis role\treference\texpected digest\texpected size"),
	}
}

func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return "line " + strconv.Itoa(i+1) + "\n want " + a + "\n got  " + b
		}
	}
	return "identical lines, different bytes"
}
