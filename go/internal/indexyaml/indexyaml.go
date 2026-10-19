// Package indexyaml reads the YAML subset that application index files use:
// block mappings, block sequences of scalars, plain and quoted scalars,
// literal and folded block scalars, and comments.
//
// Empty flow collections ([] and {}) are read as an empty sequence or
// mapping. Every other construct is an error: other flow collections, anchors, aliases,
// tags, directives, multiple documents, multi-line plain scalars and tab
// indentation. An index file that uses them is reported malformed rather
// than read by a guess.
package indexyaml

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind is the shape of a Node.
type Kind int

const (
	Null Kind = iota
	Scalar
	Map
	Seq
)

// Node is one parsed value. Keys keeps a mapping's keys in file order.
type Node struct {
	Kind  Kind
	Value string
	Keys  []string
	Map   map[string]*Node
	Items []*Node
}

// Get returns a mapping's value for key, or nil.
func (n *Node) Get(key string) *Node {
	if n == nil || n.Kind != Map {
		return nil
	}
	return n.Map[key]
}

// String returns a scalar's text and whether the node is a scalar.
func (n *Node) String() (string, bool) {
	if n == nil || n.Kind != Scalar {
		return "", false
	}
	return n.Value, true
}

const maxDepth = 32

type line struct {
	num    int
	indent int
	text   string // the line without its indentation
	blank  bool   // empty or comment only
}

type parser struct {
	lines []line
	pos   int
	noEOL bool // the last line has no line break
}

// Parse reads a document. An empty document is a Null node.
func Parse(b []byte) (*Node, error) {
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("not UTF-8")
	}
	s := strings.TrimPrefix(string(b), string(rune(0xFEFF)))
	for i, r := range s {
		if !printable(r) {
			return nil, fmt.Errorf("line %d: non-printable character %U", strings.Count(s[:i], "\n")+1, r)
		}
	}
	raw := strings.Split(s, "\n")
	p := &parser{}
	if raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	} else {
		p.noEOL = true
	}
	started := false
	for i, r := range raw {
		// YAML 1.2 (5.4) makes a lone CR a line break; this subset reads
		// LF and CRLF breaks only and rejects every lone CR, including a
		// final one with no LF after it.
		if p.noEOL && i == len(raw)-1 && strings.HasSuffix(r, "\r") {
			return nil, fmt.Errorf("line %d: lone carriage return line break", i+1)
		}
		r = strings.TrimSuffix(r, "\r")
		if strings.ContainsRune(r, '\r') {
			return nil, fmt.Errorf("line %d: carriage return inside a line", i+1)
		}
		ind := 0
		for ind < len(r) && r[ind] == ' ' {
			ind++
		}
		rest := r[ind:]
		trimmed := strings.TrimRight(rest, " \t")
		l := line{num: i + 1, indent: ind, text: rest, blank: trimmed == "" || trimmed[0] == '#'}
		if !l.blank {
			if rest[0] == '\t' {
				return nil, fmt.Errorf("line %d: tab indentation", l.num)
			}
			if ind == 0 && (trimmed == "---" || strings.HasPrefix(trimmed, "--- ")) {
				if started || strings.TrimSpace(trimmed[3:]) != "" {
					return nil, fmt.Errorf("line %d: multiple documents are not supported", l.num)
				}
				started = true
				continue
			}
			if ind == 0 && (trimmed == "..." || rest[0] == '%') {
				return nil, fmt.Errorf("line %d: directives and document markers are not supported", l.num)
			}
			started = true
		}
		p.lines = append(p.lines, l)
	}
	p.skipBlank()
	if p.pos == len(p.lines) {
		return &Node{Kind: Null}, nil
	}
	first := p.lines[p.pos]
	if first.indent != 0 {
		return nil, fmt.Errorf("line %d: document starts indented", first.num)
	}
	n, err := p.block(0, 0)
	if err != nil {
		return nil, err
	}
	p.skipBlank()
	if p.pos != len(p.lines) {
		return nil, fmt.Errorf("line %d: unexpected indentation", p.lines[p.pos].num)
	}
	return n, nil
}

// printable is YAML's c-printable set.
func printable(r rune) bool {
	switch {
	case r == '\t', r == '\n', r == '\r', r == 0x85:
		return true
	case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
		return false
	case r >= 0xd800 && r <= 0xdfff, r == 0xfffe, r == 0xffff:
		return false
	}
	return true
}

func (p *parser) skipBlank() {
	for p.pos < len(p.lines) && p.lines[p.pos].blank {
		p.pos++
	}
}

// block parses a mapping or sequence whose entries sit at indent.
func (p *parser) block(indent, depth int) (*Node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("line %d: nesting too deep", p.lines[p.pos].num)
	}
	l := p.lines[p.pos]
	if isSeqItem(l.text) {
		return p.seq(indent, depth, false)
	}
	return p.mapping(indent, depth)
}

func isSeqItem(t string) bool { return t == "-" || strings.HasPrefix(t, "- ") }

// seq parses a block sequence. underKey marks a sequence written at its
// mapping key's own indentation; it ends at the next sibling key.
func (p *parser) seq(indent, depth int, underKey bool) (*Node, error) {
	n := &Node{Kind: Seq}
	for {
		p.skipBlank()
		if p.pos == len(p.lines) || p.lines[p.pos].indent < indent {
			return n, nil
		}
		l := p.lines[p.pos]
		if l.indent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation", l.num)
		}
		if !isSeqItem(l.text) {
			if underKey && len(n.Items) > 0 {
				return n, nil
			}
			return nil, fmt.Errorf("line %d: mapping entry inside a sequence", l.num)
		}
		p.pos++
		v := strings.TrimLeft(l.text[1:], " ")
		item, err := p.value(l, v, indent, depth, false)
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, item)
	}
}

func (p *parser) mapping(indent, depth int) (*Node, error) {
	n := &Node{Kind: Map, Map: map[string]*Node{}}
	for {
		p.skipBlank()
		if p.pos == len(p.lines) || p.lines[p.pos].indent < indent {
			return n, nil
		}
		l := p.lines[p.pos]
		if l.indent > indent {
			return nil, fmt.Errorf("line %d: unexpected indentation", l.num)
		}
		if isSeqItem(l.text) {
			return nil, fmt.Errorf("line %d: sequence entry inside a mapping", l.num)
		}
		p.pos++
		key, rest, err := splitKey(l)
		if err != nil {
			return nil, err
		}
		if _, dup := n.Map[key]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %q", l.num, key)
		}
		v, err := p.value(l, rest, indent, depth, true)
		if err != nil {
			return nil, err
		}
		n.Keys = append(n.Keys, key)
		n.Map[key] = v
	}
}

// splitKey separates "key: rest" into the key and the text after the colon.
func splitKey(l line) (string, string, error) {
	t := l.text
	switch t[0] {
	case '"', '\'':
		k, end, err := quoted(t, l.num)
		if err != nil {
			return "", "", err
		}
		r := strings.TrimLeft(t[end:], " \t")
		if !strings.HasPrefix(r, ":") || (len(r) > 1 && r[1] != ' ' && r[1] != '\t') {
			return "", "", fmt.Errorf("line %d: expected ':' after key", l.num)
		}
		return k, strings.TrimLeft(r[1:], " \t"), nil
	case '?', '[', '{', ']', '}', '&', '*', '!', '|', '>', '@', '`', '%', ',':
		return "", "", fmt.Errorf("line %d: unsupported key syntax", l.num)
	}
	i := -1
	for j := 0; j+1 < len(t); j++ {
		if t[j] == ':' && (t[j+1] == ' ' || t[j+1] == '\t') {
			i = j
			break
		}
	}
	if i < 0 {
		trimmed := stripComment(t)
		if !strings.HasSuffix(trimmed, ":") {
			return "", "", fmt.Errorf("line %d: expected 'key: value'", l.num)
		}
		i = len(trimmed) - 1
	}
	k := strings.TrimRight(t[:i], " \t")
	if k == "" || strings.Contains(k, " #") || strings.Contains(k, "\t#") {
		return "", "", fmt.Errorf("line %d: invalid key", l.num)
	}
	return k, strings.TrimLeft(t[i+1:], " \t"), nil
}

// value parses what follows a key or sequence dash. inMap allows a nested
// collection on the following, more indented lines.
func (p *parser) value(l line, v string, indent, depth int, inMap bool) (*Node, error) {
	v = strings.TrimRight(v, " \t")
	if v == "" || v[0] == '#' {
		p.skipBlank()
		if p.pos < len(p.lines) {
			next := p.lines[p.pos]
			if next.indent > indent {
				return p.block(next.indent, depth+1)
			}
			if inMap && next.indent == indent && isSeqItem(next.text) {
				if depth+1 > maxDepth {
					return nil, fmt.Errorf("line %d: nesting too deep", next.num)
				}
				return p.seq(indent, depth+1, true)
			}
		}
		return &Node{Kind: Null}, nil
	}
	switch v[0] {
	case '"', '\'':
		s, end, err := quoted(v, l.num)
		if err != nil {
			return nil, err
		}
		if r := strings.TrimLeft(v[end:], " \t"); r != "" && r[0] != '#' {
			return nil, fmt.Errorf("line %d: text after quoted scalar", l.num)
		}
		return &Node{Kind: Scalar, Value: s}, p.noContinuation(indent)
	case '|', '>':
		return p.blockScalar(l, v, indent)
	}
	switch stripComment(v) {
	case "[]":
		return &Node{Kind: Seq}, p.noContinuation(indent)
	case "{}":
		return &Node{Kind: Map, Map: map[string]*Node{}}, p.noContinuation(indent)
	}
	switch v[0] {
	case '[', '{', '&', '*', '!', '@', '`', '%', '?', ',', ']', '}':
		return nil, fmt.Errorf("line %d: unsupported YAML syntax %q", l.num, v[:1])
	}
	if v == "-" || strings.HasPrefix(v, "- ") {
		return nil, fmt.Errorf("line %d: compact sequence is not supported", l.num)
	}
	s := stripComment(v)
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") {
		return nil, fmt.Errorf("line %d: mapping inside a plain scalar", l.num)
	}
	if s == "~" || s == "null" || s == "Null" || s == "NULL" {
		return &Node{Kind: Null}, p.noContinuation(indent)
	}
	return &Node{Kind: Scalar, Value: s}, p.noContinuation(indent)
}

// noContinuation rejects multi-line plain or quoted scalars.
func (p *parser) noContinuation(indent int) error {
	p.skipBlank()
	if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
		return fmt.Errorf("line %d: multi-line scalar is not supported", p.lines[p.pos].num)
	}
	return nil
}

func stripComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "\t#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t")
}

// quoted reads a single- or double-quoted scalar at the start of s and
// returns its value and the index after the closing quote.
func quoted(s string, num int) (string, int, error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if q == '\'' {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				return b.String(), i + 1, nil
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '"':
			return b.String(), i + 1, nil
		case '\\':
			if i+1 >= len(s) {
				return "", 0, fmt.Errorf("line %d: unterminated escape", num)
			}
			i++
			switch e := s[i]; e {
			case '\\', '"', '/', ' ':
				b.WriteByte(e)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '0':
				b.WriteByte(0)
			case 'x', 'u', 'U':
				n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				if i+n >= len(s) {
					return "", 0, fmt.Errorf("line %d: short \\%c escape", num, e)
				}
				r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
				if err != nil || !utf8.ValidRune(rune(r)) {
					return "", 0, fmt.Errorf("line %d: invalid \\%c escape", num, e)
				}
				b.WriteRune(rune(r))
				i += n
			default:
				return "", 0, fmt.Errorf("line %d: unsupported escape \\%c", num, e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, fmt.Errorf("line %d: unterminated quoted scalar", num)
}

// blockScalar reads a literal (|) or folded (>) scalar with an optional
// chomping indicator (+ or -) and indentation digit.
func (p *parser) blockScalar(l line, v string, indent int) (*Node, error) {
	folded := v[0] == '>'
	chomp := byte(0)
	explicit := 0
	h := stripComment(v[1:])
	for i := 0; i < len(h); i++ {
		switch c := h[i]; {
		case (c == '+' || c == '-') && chomp == 0:
			chomp = c
		case c >= '1' && c <= '9' && explicit == 0:
			explicit = int(c - '0')
		default:
			return nil, fmt.Errorf("line %d: invalid block scalar header", l.num)
		}
	}
	content := -1
	if explicit > 0 {
		content = indent + explicit
	}
	type bline struct {
		text  string
		empty bool
	}
	var out []bline
	leading := 0 // the most indentation among empty lines before the first text line
	for p.pos < len(p.lines) {
		nl := p.lines[p.pos]
		if nl.text == "" {
			if content >= 0 && nl.indent > content {
				// Spaces beyond the content indentation are text.
				out = append(out, bline{text: strings.Repeat(" ", nl.indent-content)})
			} else {
				if content < 0 && nl.indent > leading {
					leading = nl.indent
				}
				out = append(out, bline{empty: true})
			}
			p.pos++
			continue
		}
		if content < 0 {
			if nl.indent <= indent {
				break
			}
			content = nl.indent
			if leading > content {
				return nil, fmt.Errorf("line %d: block scalar empty line more indented than its first line", l.num)
			}
		}
		if nl.indent < content {
			if nl.indent > indent {
				return nil, fmt.Errorf("line %d: block scalar line less indented than its first line", nl.num)
			}
			break
		}
		out = append(out, bline{text: strings.Repeat(" ", nl.indent-content) + nl.text})
		p.pos++
	}
	// The last line of a file without a final line break contributes no
	// line feed.
	lastNoBreak := p.pos == len(p.lines) && p.noEOL && len(out) > 0
	trail := 0
	for trail < len(out) && out[len(out)-1-trail].empty {
		trail++
	}
	body := out[:len(out)-trail]
	var b strings.Builder
	if folded {
		prev, k := -1, 0
		for i, t := range body {
			if t.empty {
				k++
				continue
			}
			if prev < 0 {
				b.WriteString(strings.Repeat("\n", k))
			} else {
				spaced := moreIndented(body[prev].text) || moreIndented(t.text)
				switch {
				case spaced:
					b.WriteString(strings.Repeat("\n", k+1))
				case k == 0:
					b.WriteByte(' ')
				default:
					b.WriteString(strings.Repeat("\n", k))
				}
			}
			b.WriteString(t.text)
			prev, k = i, 0
		}
	} else {
		for i, t := range body {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(t.text)
		}
	}
	s := b.String()
	breaks := trail // line feeds after the last text line
	if len(body) > 0 {
		breaks++
	}
	if lastNoBreak {
		breaks--
	}
	switch {
	case chomp == '+':
		s += strings.Repeat("\n", breaks)
	case len(body) > 0 && chomp == 0 && breaks > 0:
		s += "\n"
	}
	return &Node{Kind: Scalar, Value: s}, nil
}

func moreIndented(t string) bool { return t != "" && (t[0] == ' ' || t[0] == '\t') }
