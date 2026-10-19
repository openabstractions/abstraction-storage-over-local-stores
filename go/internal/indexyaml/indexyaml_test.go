package indexyaml

import (
	"strings"
	"testing"
)

func TestReadsSubset(t *testing.T) {
	doc := "---\n# comment\na111:\n    base_path: C:\\sd # trailing\n\n    is_default: true\n    loras: |\n         models/Lora\n         models/LyCORIS\n    keep: |+\n      x\n\n    fold: >-\n      a\n      b\n\n      c\n    q1: 'it''s'\n    q2: \"a\\tb\\u00e9\"\n    empty:\n    list:\n    - one\n    - 'two'\nother: ~\n"
	n, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	a := n.Get("a111")
	want := map[string]string{
		"base_path":  `C:\sd`,
		"is_default": "true",
		"loras":      "models/Lora\nmodels/LyCORIS\n",
		"keep":       "x\n\n",
		"fold":       "a b\nc",
		"q1":         "it's",
		"q2":         "a\tb\u00e9",
	}
	for k, w := range want {
		if got, ok := a.Get(k).String(); !ok || got != w {
			t.Errorf("%s = %q, %v; want %q", k, got, ok, w)
		}
	}
	if a.Get("empty").Kind != Null || n.Get("other").Kind != Null {
		t.Error("expected null")
	}
	if l := a.Get("list"); l.Kind != Seq || len(l.Items) != 2 || l.Items[1].Value != "two" {
		t.Errorf("list = %+v", l)
	}
	if strings.Join(n.Keys, ",") != "a111,other" {
		t.Errorf("keys = %v", n.Keys)
	}
}

// TestScalarValues pins values against what YAML 1.2 (yaml.v3 v3.0.1) reads.
func TestScalarValues(t *testing.T) {
	for doc, want := range map[string]string{
		"a: |+\n  x\n":         "x\n",
		"a: |+\n  x\n\n":       "x\n\n",
		"0: |\n 00":            "00",
		"0: |+\n 00":           "00",
		"0: |\n 00\n":          "00\n",
		"a: >\n\n  b\n":        "\nb\n",
		"a: >\n  a\n\n    b\n": "a\n\n  b\n",
		"a: |\n  x\n    \n":    "x\n  \n",
		"a: |-\n  x\n\n":       "x",
		"a: |\n\n":             "",
		"a: |+\n\n\n":          "\n\n",
	} {
		n, err := Parse([]byte(doc))
		if err != nil {
			t.Errorf("%q: %v", doc, err)
			continue
		}
		if got, ok := n.Get(firstKey(n)).String(); !ok || got != want {
			t.Errorf("%q = %q, %v; want %q", doc, got, ok, want)
		}
	}
}

func firstKey(n *Node) string {
	if n == nil || len(n.Keys) == 0 {
		return ""
	}
	return n.Keys[0]
}

func TestEmptyFlowAndSequenceUnderKey(t *testing.T) {
	n, err := Parse([]byte("list:\n- one\nother: x\nseq: []\nmap: {} # empty\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l := n.Get("list"); l.Kind != Seq || len(l.Items) != 1 || l.Items[0].Value != "one" {
		t.Errorf("list = %+v", l)
	}
	if v, _ := n.Get("other").String(); v != "x" {
		t.Errorf("other = %q", v)
	}
	if s := n.Get("seq"); s.Kind != Seq || len(s.Items) != 0 {
		t.Errorf("seq = %+v", s)
	}
	if m := n.Get("map"); m.Kind != Map || len(m.Map) != 0 || m.Get("x") != nil {
		t.Errorf("map = %+v", m)
	}
	if strings.Join(n.Keys, ",") != "list,other,seq,map" {
		t.Errorf("keys = %v", n.Keys)
	}
}

func TestRejectsOutsideSubset(t *testing.T) {
	for _, doc := range []string{
		"a: [x]\n", "a: {b: c}\n", "a: &x b\n", "a: *x\n", "a: !!str b\n",
		"%YAML 1.2\n---\na: b\n", "a: b\n---\nc: d\n", "a: b\n  c\n",
		"a: b\na: c\n", "\ta: b\n", "a:\n\tb: c\n", "a: \"open\n", "base_path: [unclosed\n",
		"name: : :\n", "a\n", "a: b\n - c\n", "? a\n: b\n", "a: - b\n",
		"- a\nb: c\n", "a: [\n", "a: [[]]\n", "a: []x\n", "a: {}\n  b\n",
		"0: 0\x10\n", "a: b\x7f\n", "]a: b\n", "}a: b\n",
		"a: |\n    \n  x\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("accepted %q", doc)
		}
	}
}

func TestTabSeparation(t *testing.T) {
	n, err := Parse([]byte("0: \t0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := n.Get("0").String(); v != "0" {
		t.Errorf("value after tab: %q", v)
	}
	n, err = Parse([]byte("k\t: v\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := n.Get("k").String(); v != "v" || len(n.Keys) != 1 || n.Keys[0] != "k" {
		t.Errorf("key before tab: %q %q", n.Keys, v)
	}
	if _, err := Parse([]byte("a: b\r")); err == nil {
		t.Error("accepted a final lone CR")
	}
	if _, err := Parse([]byte("a: b\rc: d\n")); err == nil {
		t.Error("accepted an inner lone CR")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"a:\n  b: c\n", "a: |\n  x\n", "- a\n- b\n", "a: 'x'\n", "a: \"\\u00e9\"\n", "a: >+\n\n  b\n\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		n, err := Parse(b)
		if err != nil {
			return
		}
		if n == nil {
			t.Fatal("nil node without error")
		}
		check(t, n)
	})
}

// check asserts a parsed tree's shape and that its accessors do not panic.
func check(t *testing.T, n *Node) {
	t.Helper()
	n.String()
	n.Get("a")
	switch n.Kind {
	case Map:
		if len(n.Keys) != len(n.Map) || n.Items != nil {
			t.Fatalf("map shape %+v", n)
		}
		for _, k := range n.Keys {
			c := n.Map[k]
			if c == nil {
				t.Fatalf("key %q has no node", k)
			}
			check(t, c)
		}
	case Seq:
		if n.Map != nil || n.Keys != nil {
			t.Fatalf("seq shape %+v", n)
		}
		for _, c := range n.Items {
			if c == nil {
				t.Fatal("nil item")
			}
			check(t, c)
		}
	case Scalar, Null:
		if n.Map != nil || n.Items != nil || n.Keys != nil {
			t.Fatalf("leaf shape %+v", n)
		}
	default:
		t.Fatalf("kind %d", n.Kind)
	}
}
