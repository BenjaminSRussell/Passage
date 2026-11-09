package ebics

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	nsXML         = "http://www.w3.org/XML/1998/namespace"
	maxMessageLen = 4 << 20
	maxDepth      = 64
)

type xattr struct{ prefix, local, value string }

type xnode struct {
	prefix, local string
	attrs         []xattr
	scope         map[string]string
	kids          []*xnode
	text          string
	isText        bool
}

func (n *xnode) attr(local string) string {
	for _, a := range n.attrs {
		if a.prefix == "" && a.local == local {
			return a.value
		}
	}
	return ""
}

func (n *xnode) child(local string) *xnode {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if !k.isText && k.local == local {
			return k
		}
	}
	return nil
}

func (n *xnode) path(locals ...string) *xnode {
	for _, l := range locals {
		n = n.child(l)
	}
	return n
}

func (n *xnode) content() string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	for _, k := range n.kids {
		if k.isText {
			b.WriteString(k.text)
		}
	}
	return strings.TrimSpace(b.String())
}

func (n *xnode) uri() string {
	return n.scope[n.prefix]
}

func parseXML(data []byte) (*xnode, error) {
	if len(data) > maxMessageLen {
		return nil, errors.New("xml message too large")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	var root *xnode
	var stack []*xnode
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) >= maxDepth {
				return nil, errors.New("xml: nesting too deep")
			}
			n := &xnode{prefix: t.Name.Space, local: t.Name.Local, scope: map[string]string{"xml": nsXML}}
			if len(stack) > 0 {
				for p, u := range stack[len(stack)-1].scope {
					n.scope[p] = u
				}
			}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					n.scope[""] = a.Value
				case a.Name.Space == "xmlns":
					n.scope[a.Name.Local] = a.Value
				default:
					n.attrs = append(n.attrs, xattr{a.Name.Space, a.Name.Local, a.Value})
				}
			}
			if _, ok := n.scope[n.prefix]; !ok && n.prefix != "" {
				return nil, fmt.Errorf("xml: unbound prefix %q", n.prefix)
			}
			for _, a := range n.attrs {
				if _, ok := n.scope[a.prefix]; !ok && a.prefix != "" {
					return nil, fmt.Errorf("xml: unbound prefix %q", a.prefix)
				}
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("xml: more than one root element")
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("xml: unexpected end element")
			}
			if top := stack[len(stack)-1]; top.prefix != t.Name.Space || top.local != t.Name.Local {
				return nil, fmt.Errorf("xml: </%s> closes <%s>", qname(t.Name.Space, t.Name.Local), qname(top.prefix, top.local))
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, &xnode{isText: true, text: string(t)})
			}
		case xml.Directive:
			return nil, errors.New("xml: DOCTYPE and other directives are not accepted")
		case xml.ProcInst:
			if t.Target != "xml" {
				return nil, errors.New("xml: processing instructions are not accepted")
			}
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, errors.New("xml: incomplete document")
	}
	return root, nil
}

// Canonicalize returns the Canonical XML 1.0 (without comments) form of the
// document: https://www.w3.org/TR/2001/REC-xml-c14n-20010315.
func Canonicalize(data []byte) ([]byte, error) {
	root, err := parseXML(data)
	if err != nil {
		return nil, err
	}
	return canonicalNodes([]*xnode{root}), nil
}

// canonicalNodes writes each apex element with its subtree, in the order given,
// as inclusive C14N does for a node set made of whole subtrees: every in-scope
// namespace is rendered on the apex because none of its ancestors is output.
func canonicalNodes(apexes []*xnode) []byte {
	var b bytes.Buffer
	for _, n := range apexes {
		writeCanonical(&b, n, map[string]string{})
	}
	return b.Bytes()
}

func writeCanonical(b *bytes.Buffer, n *xnode, rendered map[string]string) {
	prefixes := []string{""}
	for p := range n.scope {
		if p != "" && p != "xml" {
			prefixes = append(prefixes, p)
		}
	}
	sort.Strings(prefixes)
	next := make(map[string]string, len(rendered)+len(prefixes))
	for p, u := range rendered {
		next[p] = u
	}
	b.WriteByte('<')
	b.WriteString(qname(n.prefix, n.local))
	for _, p := range prefixes {
		if n.scope[p] == rendered[p] {
			continue
		}
		next[p] = n.scope[p]
		if p == "" {
			b.WriteString(` xmlns="`)
		} else {
			b.WriteString(` xmlns:` + p + `="`)
		}
		escapeAttr(b, n.scope[p])
		b.WriteByte('"')
	}
	attrs := append([]xattr(nil), n.attrs...)
	sort.Slice(attrs, func(i, j int) bool {
		ui, uj := attrURI(n, attrs[i]), attrURI(n, attrs[j])
		if ui != uj {
			return ui < uj
		}
		return attrs[i].local < attrs[j].local
	})
	for _, a := range attrs {
		b.WriteString(" " + qname(a.prefix, a.local) + `="`)
		escapeAttr(b, a.value)
		b.WriteByte('"')
	}
	b.WriteByte('>')
	for _, k := range n.kids {
		if k.isText {
			escapeText(b, k.text)
		} else {
			writeCanonical(b, k, next)
		}
	}
	b.WriteString("</" + qname(n.prefix, n.local) + ">")
}

func attrURI(n *xnode, a xattr) string {
	if a.prefix == "" {
		return ""
	}
	return n.scope[a.prefix]
}

func qname(prefix, local string) string {
	if prefix == "" {
		return local
	}
	return prefix + ":" + local
}

func escapeText(b *bytes.Buffer, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteRune(r)
		}
	}
}

func escapeAttr(b *bytes.Buffer, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '"':
			b.WriteString("&quot;")
		case '\t':
			b.WriteString("&#x9;")
		case '\n':
			b.WriteString("&#xA;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteRune(r)
		}
	}
}

// authenticated returns the elements EBICS signs: those with authenticate="true"
// that are not inside another such element, in document order.
func authenticated(root *xnode) []*xnode {
	var out []*xnode
	var walk func(n *xnode)
	walk = func(n *xnode) {
		if n.isText {
			return
		}
		if n.attr("authenticate") == "true" {
			out = append(out, n)
			return
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	walk(root)
	return out
}
