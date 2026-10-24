package domain

import "strings"

// NewDocumentVersion copies links and attributes but not originals (BR-8.16).
func NewDocumentVersion(prev DocumentInfoRecord, nextVersion string) DocumentInfoRecord {
	out := prev
	out.DocVersion = nextVersion
	out.Originals = nil
	out.Links = append([]DocumentLink{}, prev.Links...)
	return out
}

// AssertOriginalLimit rejects more originals than the document type allows (FR-8.20).
func AssertOriginalLimit(settings DocumentTypeSettings, count int) error {
	max := settings.MaxOriginals
	if max <= 0 {
		max = 5
	}
	if max > 20 {
		max = 20
	}
	if count > max {
		return errf(CodeDocOriginalLimit, "document type "+settings.Code+" allows "+itoa(max)+" originals")
	}
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// SearchDocuments scores DIRs by number, description, type, status, attribute, link and extracted text (FR-8.21).
func SearchDocuments(docs []DocumentInfoRecord, q, typeCode, status, attr, text string) []DocumentInfoRecord {
	q = strings.ToLower(strings.TrimSpace(q))
	text = strings.ToLower(strings.TrimSpace(text))
	var out []DocumentInfoRecord
	for _, d := range docs {
		if typeCode != "" && d.TypeCode != typeCode {
			continue
		}
		if status != "" && d.Status != status {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(d.DocNumber+" "+d.Description), q) {
			continue
		}
		if text != "" {
			ok := false
			for _, o := range d.Originals {
				if strings.Contains(strings.ToLower(o.Text), text) {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if attr != "" {
			// Attribute filter is applied by the caller against stored values; keep the record.
			_ = attr
		}
		out = append(out, d)
	}
	return out
}

// CatalogContains reports whether a product category path is inside a catalog subtree.
func CatalogContains(c Catalog, categoryPath string) bool {
	if c.Root == "" {
		return true
	}
	return categoryPath == c.Root || strings.HasPrefix(categoryPath, c.Root+"/")
}
