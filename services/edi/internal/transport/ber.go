package transport

import (
	"bytes"
	"errors"
)

// berToDER rewrites a BER encoding with indefinite lengths (as streaming CMS
// writers such as OpenSSL produce) into definite-length DER that
// encoding/asn1 reads. Constructed OCTET STRINGs are flattened into one
// primitive OCTET STRING. Nothing else is canonicalised: a DER input comes
// back byte for byte, so signatures over DER-encoded parts still verify.
func berToDER(in []byte) ([]byte, error) {
	out, rest, err := berElement(in, 0)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimRight(rest, "\x00")) != 0 {
		return nil, errors.New("transport: trailing data after the ASN.1 value")
	}
	return out, nil
}

const maxBERDepth = 64

var errBER = errors.New("transport: malformed BER")

// berElement reads one TLV from in and returns its DER form and the rest.
func berElement(in []byte, depth int) (out, rest []byte, err error) {
	if depth > maxBERDepth {
		return nil, nil, errBER
	}
	if len(in) < 2 {
		return nil, nil, errBER
	}
	// Identifier octets (high tag numbers span several bytes).
	i := 1
	if in[0]&0x1f == 0x1f {
		for i < len(in) && in[i]&0x80 != 0 {
			i++
		}
		i++
		if i >= len(in) {
			return nil, nil, errBER
		}
	}
	ident := in[:i]
	constructed := in[0]&0x20 != 0
	lb := in[i]
	i++
	var content []byte
	switch {
	case lb == 0x80:
		if !constructed {
			return nil, nil, errBER
		}
		body := in[i:]
		var children [][]byte
		for {
			if len(body) >= 2 && body[0] == 0 && body[1] == 0 {
				body = body[2:]
				break
			}
			child, r, err := berElement(body, depth+1)
			if err != nil {
				return nil, nil, err
			}
			children = append(children, child)
			body = r
		}
		content, rest = bytes.Join(children, nil), body
	case lb&0x80 == 0:
		n := int(lb)
		if len(in)-i < n {
			return nil, nil, errBER
		}
		content, rest = in[i:i+n], in[i+n:]
		if constructed {
			var children [][]byte
			body := content
			for len(body) > 0 {
				child, r, err := berElement(body, depth+1)
				if err != nil {
					return nil, nil, err
				}
				children = append(children, child)
				body = r
			}
			content = bytes.Join(children, nil)
		}
	default:
		k := int(lb & 0x7f)
		if k > 4 || len(in)-i < k {
			return nil, nil, errBER
		}
		n := 0
		for _, b := range in[i : i+k] {
			n = n<<8 | int(b)
		}
		i += k
		if n < 0 || len(in)-i < n {
			return nil, nil, errBER
		}
		content, rest = in[i:i+n], in[i+n:]
		if constructed {
			var children [][]byte
			body := content
			for len(body) > 0 {
				child, r, err := berElement(body, depth+1)
				if err != nil {
					return nil, nil, err
				}
				children = append(children, child)
				body = r
			}
			content = bytes.Join(children, nil)
		}
	}
	// A constructed universal OCTET STRING becomes one primitive OCTET STRING.
	if len(ident) == 1 && ident[0] == 0x24 {
		flat, err := concatOctets(content)
		if err != nil {
			return nil, nil, err
		}
		return derTLV([]byte{0x04}, flat), rest, nil
	}
	return derTLV(ident, content), rest, nil
}

// concatOctets joins the contents of a sequence of DER OCTET STRINGs.
func concatOctets(der []byte) ([]byte, error) {
	var out []byte
	for len(der) > 0 {
		if der[0] != 0x04 {
			return nil, errBER
		}
		content, rest, err := derContent(der)
		if err != nil {
			return nil, err
		}
		out = append(out, content...)
		der = rest
	}
	return out, nil
}

// derContent splits one definite-length TLV with a one-byte identifier.
func derContent(der []byte) (content, rest []byte, err error) {
	if len(der) < 2 {
		return nil, nil, errBER
	}
	lb, i := der[1], 2
	n := int(lb)
	if lb&0x80 != 0 {
		k := int(lb & 0x7f)
		if k == 0 || k > 4 || len(der) < 2+k {
			return nil, nil, errBER
		}
		n = 0
		for _, b := range der[2 : 2+k] {
			n = n<<8 | int(b)
		}
		i += k
	}
	if n < 0 || len(der)-i < n {
		return nil, nil, errBER
	}
	return der[i : i+n], der[i+n:], nil
}

func derTLV(ident, content []byte) []byte {
	out := append([]byte(nil), ident...)
	n := len(content)
	switch {
	case n < 0x80:
		out = append(out, byte(n))
	case n < 0x100:
		out = append(out, 0x81, byte(n))
	case n < 0x10000:
		out = append(out, 0x82, byte(n>>8), byte(n))
	case n < 0x1000000:
		out = append(out, 0x83, byte(n>>16), byte(n>>8), byte(n))
	default:
		out = append(out, 0x84, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(out, content...)
}
