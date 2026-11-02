package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// lifecycle is the order a product moves through (BR-8.1).
var lifecycle = []string{"draft", "active", "phase_out", "obsolete"}

const attrPrefix = "attr."

var (
	productFields   = []string{"productNumber", "subtype", "productType", "name", "description", "searchName", "baseUom", "dimensionGroupId", "lifecycleState"}
	productEditable = []string{"name", "description", "searchName"}
)

// productReceipt is what the product service answers a create or a PATCH with (its JSON keys are the Go field names,
// which encoding/json matches case-insensitively).
type productReceipt struct {
	ID             string `json:"id"`
	Version        int    `json:"version"`
	LifecycleState string `json:"lifecycleState"`
}

// createKey is the Idempotency-Key of a product create. It is the id the pipeline reserved for the legacy key, not
// the row's content: the product service answers a repeated key with the product as it is now (its lifecycle state and
// version), so a rerun after a refused activation, a crash or an edit of the legacy row picks the earlier draft up
// instead of colliding with it on its id or product number.
func createKey(targetID string) string {
	sum := sha256.Sum256([]byte("migration-loader|product|create|" + targetID))
	return hex.EncodeToString(sum[:])
}

// lifecyclePath is the PATCHes that move a product from one lifecycle state to another: one step at a time forward,
// so BR-8.1 and the required-attribute check of BR-8.9 apply to the legacy item, or the target state itself for a
// move back (phase_out to active), which the product service judges.
func lifecyclePath(from, to string) []string {
	fi, ti := slices.Index(lifecycle, from), slices.Index(lifecycle, to)
	switch {
	case fi == ti:
		return nil
	case fi < 0 || ti < fi:
		return []string{to}
	}
	return lifecycle[fi+1 : ti+1]
}

// loadProduct creates a product as a draft under the id the pipeline reserved, sets its attributes, then moves it to
// the lifecycle state the mapping names (default active). A row whose activation is refused is rejected with the
// target's code; the draft stays in Passage and the next run continues from it. A product an earlier run loaded is
// patched under the version recorded then.
func (l *Loader) loadProduct(ctx context.Context, c *call, it domain.LoadItem) (domain.LoadResult, error) {
	le, bad := l.legalEntity(it.Fields)
	if bad != nil {
		return *bad, nil
	}
	want := strings.ToLower(text(it.Fields, "lifecycleState"))
	if want == "" {
		want = "active"
	}
	if !slices.Contains(lifecycle, want) {
		return domain.LoadResult{Code: CodeFieldRequired, Detail: "lifecycleState must be draft, active, phase_out or obsolete"}, nil
	}
	attrs := map[string]string{}
	for k := range it.Fields {
		if name, ok := strings.CutPrefix(k, attrPrefix); ok && name != "" && text(it.Fields, k) != "" {
			attrs[name] = text(it.Fields, k)
		}
	}
	edit := map[string]any{}
	pick(edit, it.Fields, productEditable...)
	id := it.TargetID
	path := "/v1/product/products/" + url.PathEscape(id)
	if it.Existing {
		if it.TargetVersion < 1 {
			return domain.LoadResult{Code: CodeVersionUnknown, Detail: "the product was loaded without a recorded version; it cannot be patched"}, nil
		}
		if res, done, err := l.setAttributes(ctx, c, path, le, attrs); err != nil || done {
			return res, err
		}
		patch := maps.Clone(edit)
		patch["lifecycleState"] = want
		r, err := c.do(ctx, http.MethodPatch, path, le, map[string]string{"If-Match": etag(it.TargetVersion)}, patch)
		if err != nil {
			return domain.LoadResult{}, err
		}
		if !r.ok() {
			return r.updateRefusal(), nil
		}
		return accepted(r, id), nil
	}
	if missing := required(it.Fields, "productNumber", "name", "baseUom"); len(missing) > 0 {
		return missingResult(missing), nil
	}
	body := map[string]any{"id": id, "lifecycleState": "draft"}
	pick(body, it.Fields, "productNumber", "subtype", "productType", "name", "description", "searchName", "baseUom", "dimensionGroupId")
	r, err := c.do(ctx, http.MethodPost, "/v1/product/products", le, map[string]string{"Idempotency-Key": createKey(id)}, body)
	if err != nil {
		return domain.LoadResult{}, err
	}
	if !r.ok() {
		return r.refusal(), nil
	}
	var rc productReceipt
	_ = json.Unmarshal(r.body, &rc)
	if rc.ID != "" {
		id = rc.ID
	}
	// A fresh create answers version 0 (the product service does not read the new row back; a product starts at
	// version 1), a replayed one the version and state the product has now.
	version, state := max(rc.Version, 1), rc.LifecycleState
	if state == "" {
		state = "draft"
	}
	path = "/v1/product/products/" + url.PathEscape(id)
	if res, done, err := l.setAttributes(ctx, c, path, le, attrs); err != nil || done {
		return res, err
	}
	// The editable fields ride on the first PATCH: on a fresh create they change nothing, on a replay they carry an
	// edit of the legacy row. With no lifecycle step they go alone (a PATCH that changes nothing keeps the version).
	steps := lifecyclePath(state, want)
	if len(steps) == 0 {
		steps = []string{""}
	}
	for i, step := range steps {
		patch := map[string]any{}
		if i == 0 {
			patch = maps.Clone(edit)
		}
		if step != "" {
			patch["lifecycleState"] = step
		}
		if len(patch) == 0 {
			break
		}
		r, err := c.do(ctx, http.MethodPatch, path, le, map[string]string{"If-Match": etag(version)}, patch)
		if err != nil {
			return domain.LoadResult{}, err
		}
		if !r.ok() {
			res := r.refusal()
			if step != "" {
				res.Detail = "the product is in Passage as " + state + "; moving it to " + step + " was refused: " + res.Detail
			}
			return res, nil
		}
		var after productReceipt
		_ = json.Unmarshal(r.body, &after)
		if after.Version < 1 {
			return domain.LoadResult{Code: CodeVersionUnknown, Detail: "the product service did not answer a version; the product stays " + state}, nil
		}
		version = after.Version
		if step != "" {
			state = step
		}
	}
	return domain.LoadResult{TargetID: id, TargetVersion: version}, nil
}

// setAttributes posts the attr.* fields; done is true with the reject when the product service refused them.
func (l *Loader) setAttributes(ctx context.Context, c *call, path, le string, attrs map[string]string) (domain.LoadResult, bool, error) {
	if len(attrs) == 0 {
		return domain.LoadResult{}, false, nil
	}
	r, err := c.do(ctx, http.MethodPost, path+"/attributes", le, nil, attrs)
	if err != nil {
		return domain.LoadResult{}, false, err
	}
	if !r.ok() {
		return r.refusal(), true, nil
	}
	return domain.LoadResult{}, false, nil
}
