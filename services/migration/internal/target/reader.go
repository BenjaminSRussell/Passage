package target

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// Reader reads back, for reconciliation, the records a run's loader wrote: every cross-referenced row of the entity is
// read from the owning service's detail route by the target id the load recorded, as service:migration-loader with the
// owning service's *.import.read duty (party.import.read, product.import.read, receivables.import.read,
// engineering.import.read). A record the target no longer has is left out, so the row count check fails.
type Reader struct{ l *Loader }

var _ domain.ProjectingReader = (*Reader)(nil)

// Reader is the read-back of the loader's run.
func (l *Loader) Reader() *Reader { return &Reader{l: l} }

// readSpec says where an entity is read and which target field holds each loader field.
type readSpec struct {
	aud  string
	path func(id string) string
	// fields are the loader fields compared, each read from the target field of the same name (case-insensitive)
	// unless from names another.
	fields []string
	from   map[string]string
}

var readSpecs = map[string]readSpec{
	// dob is masked for a caller without pii.read, and status is not sent on create, so neither is compared.
	"party":    {aud: audParty, path: func(id string) string { return "/v1/party/parties/" + url.PathEscape(id) }, fields: without(partyFields, "dob")},
	"customer": {aud: audParty, path: func(id string) string { return "/v1/party/customers/" + url.PathEscape(id) }, fields: slices.Concat([]string{"partyId", "creditHold"}, customerNeed, customerOptional)},
	"vendor":   {aud: audParty, path: func(id string) string { return "/v1/party/vendors/" + url.PathEscape(id) }, fields: slices.Concat([]string{"partyId"}, vendorNeed, vendorOptional)},
	"product":  {aud: audProduct, path: productPath, fields: productFields},
	"item":     {aud: audProduct, path: productPath, fields: productFields},
	"ar_open_item": {aud: audReceivables, path: func(id string) string { return "/v1/receivables/open-items/" + url.PathEscape(id) },
		fields: slices.Concat([]string{"customerId"}, without(openItemFields, "kind")), from: map[string]string{"originalAmount": "amount"}},
	"bom":   {aud: audEngineering, path: func(id string) string { return "/v1/engineering/bom-versions/" + url.PathEscape(id) }, fields: slices.Concat([]string{"productId", "lines"}, bomFields)},
	"route": {aud: audEngineering, path: func(id string) string { return "/v1/engineering/route-versions/" + url.PathEscape(id) }, fields: slices.Concat([]string{"productId", "operations", "versionNo"}, routeFields)},
}

func productPath(id string) string { return "/v1/product/products/" + url.PathEscape(id) }

func without(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

func (l *Loader) base(aud string) string {
	switch aud {
	case audParty:
		return l.cfg.PartyURL
	case audProduct:
		return l.cfg.ProductURL
	case audReceivables:
		return l.cfg.ReceivablesURL
	case audEngineering:
		return l.cfg.EngineeringURL
	}
	return ""
}

// Reads reports whether the entity has a read-back route at a configured target. A service token names one legal
// entity, so a tenant-wide entity (party, product, BOM, route) is read under the run's; a run without one (rows that
// name their own) is not reconciled for those entities.
func (r *Reader) Reads(entity string) bool {
	spec, ok := readSpecs[entity]
	return ok && r.l.base(spec.aud) != "" && (domain.XrefLegalEntityScoped(entity) || r.l.run.LegalEntityID != "")
}

// Codes and amounts the targets normalise: compared upper-cased, and amounts as magnitudes (receivables stores a
// credit-side item with negative amounts, the loader sends magnitudes and a side).
var (
	upperFields = map[string]bool{"currencyCode": true, "creditCurrency": true, "currency": true, "baseUom": true, "lifecycleState": true,
		"partyType": true, "side": true, "subtype": true, "productType": true, "blocked": true, "usage": true, "category": true, "languageCode": true}
	magnitudeFields = map[string]bool{"originalAmount": true, "openAmount": true, "cashDiscountAmount": true}
	numberShape     = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
)

// canonical is the form both sides of a reconciliation are compared in: trimmed text, codes upper-cased, numbers in
// their shortest decimal form ("10.50" and 10.5 compare equal), "" for no value.
func canonical(field string, v any) string {
	s := text(map[string]any{field: v}, field)
	if s == "" || s == "<nil>" {
		return ""
	}
	if numberShape.MatchString(s) {
		if d, err := decimal.NewFromString(s); err == nil {
			if magnitudeFields[field] {
				d = d.Abs()
			}
			return d.String()
		}
	}
	if upperFields[field] {
		return strings.ToUpper(s)
	}
	return s
}

// Project maps a row, as mapped or as read back (both under the loader's field names), onto the compared fields of the
// entity. BOM lines and route operations are compared by their shape: each line's number and quantity, each
// operation's number and successor; the components and resources are ids on one side and may be legacy keys on the other.
func (r *Reader) Project(entity string, fields map[string]any) map[string]any {
	spec := readSpecs[entity]
	out := map[string]any{}
	for _, name := range spec.fields {
		var v string
		switch {
		case entity == "bom" && name == "lines":
			v = shape(fields[name], func(i int, m map[string]any) (int, string) {
				no, _ := strconv.Atoi(canonical("lineNo", m["lineNo"]))
				if no == 0 {
					no = i + 1
				}
				return no, strconv.Itoa(no) + "x" + canonical("quantity", m["quantity"])
			})
		case entity == "route" && name == "operations":
			v = shape(fields[name], func(_ int, m map[string]any) (int, string) {
				no, _ := strconv.Atoi(canonical("no", m["no"]))
				next := canonical("nextNo", m["nextNo"])
				if next == "" {
					next = "0"
				}
				return no, strconv.Itoa(no) + ">" + next
			})
		default:
			v = canonical(name, fields[name])
		}
		if v != "" {
			out[name] = v
		}
	}
	return out
}

// shape renders a list of objects as its entries in number order; "" when it is not a non-empty list.
func shape(v any, entry func(i int, m map[string]any) (int, string)) string {
	items, err := list(map[string]any{"v": v}, "v")
	if err != nil || len(items) == 0 {
		return ""
	}
	type numbered struct {
		no int
		s  string
	}
	rows := make([]numbered, len(items))
	for i, m := range items {
		rows[i].no, rows[i].s = entry(i, m)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].no < rows[j].no })
	parts := make([]string, len(rows))
	for i, row := range rows {
		parts[i] = row.s
	}
	return strings.Join(parts, ",")
}

// fromTarget renames a target record's fields to the loader's (target JSON keys match case-insensitively: the product
// service answers with Go field names).
func fromTarget(spec readSpec, body map[string]any) map[string]any {
	lower := make(map[string]any, len(body))
	for k, v := range body {
		lower[strings.ToLower(k)] = v
	}
	out := make(map[string]any, len(spec.fields))
	for _, name := range spec.fields {
		key := name
		if alt, ok := spec.from[name]; ok {
			key = alt
		}
		if v, ok := lower[strings.ToLower(key)]; ok {
			out[name] = v
		}
	}
	return out
}

// Read reads back every row of entity that the cross-reference records as loaded, a few at a time.
func (r *Reader) Read(ctx context.Context, entity string) ([]domain.Record, error) {
	spec, ok := readSpecs[entity]
	if !ok || r.l.base(spec.aud) == "" {
		return nil, fmt.Errorf("no read-back route for %s", entity)
	}
	p, ok := tenant.FromContext(ctx)
	if !ok || p.TenantID == "" {
		return nil, errors.New("reader needs the tenant in the context")
	}
	xref := r.l.run.Xref
	if xref == nil {
		return nil, errors.New("reader needs the run's cross-reference")
	}
	keys, err := xref.Keys(ctx, r.l.run.SourceSystem, entity)
	if err != nil {
		return nil, err
	}
	entries, err := xref.GetMany(ctx, r.l.run.SourceSystem, entity, keys)
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	type job struct {
		key string
		e   domain.XrefEntry
	}
	var jobs []job
	for _, k := range keys {
		if e, ok := entries[k]; ok && e.ContentHash != "" && e.TargetID != "" {
			jobs = append(jobs, job{k, e})
		}
	}
	out := make([]*domain.Record, len(jobs))
	c := &call{l: r.l, tenantID: p.TenantID, aud: spec.aud, base: r.l.base(spec.aud)}
	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	sem := make(chan struct{}, r.l.cfg.Concurrency)
	for i, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			defer func() { <-sem }()
			le := j.e.LegalEntityID
			if le == "" {
				le = strings.ToLower(r.l.run.LegalEntityID)
			}
			if le == "" {
				errOnce.Do(func() { firstErr = fmt.Errorf("read %s %s: no legal entity to read it in", entity, j.key) })
				return
			}
			rec, err := r.readOne(ctx, c, spec, entity, j.key, j.e.TargetID, le)
			if err != nil {
				errOnce.Do(func() { firstErr = err })
				return
			}
			out[i] = rec
		}(i, j)
	}
	wg.Wait()
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	if firstErr != nil {
		return nil, firstErr
	}
	records := make([]domain.Record, 0, len(out))
	for _, rec := range out {
		if rec != nil {
			records = append(records, *rec)
		}
	}
	return records, nil
}

// readOne reads one target record; nil when the target does not have it (404).
func (r *Reader) readOne(ctx context.Context, c *call, spec readSpec, entity, key, id, legalEntity string) (*domain.Record, error) {
	rep, err := c.do(ctx, http.MethodGet, spec.path(id), legalEntity, nil, nil)
	switch {
	case err != nil:
		return nil, err
	case rep.exhausted:
		return nil, fmt.Errorf("read %s %s: %s", entity, key, rep.detail)
	case rep.status == http.StatusNotFound:
		return nil, nil
	case !rep.ok():
		code, detail := problem(rep.status, rep.body)
		return nil, fmt.Errorf("read %s %s: %s %s", entity, key, code, detail)
	}
	dec := json.NewDecoder(bytes.NewReader(rep.body))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("read %s %s: the answer is not a JSON object", entity, key)
	}
	return &domain.Record{Key: key, Fields: r.Project(entity, fromTarget(spec, body))}, nil
}
