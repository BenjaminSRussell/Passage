package api

import (
	"fmt"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

type storedRollRequest struct {
	SiteID  string             `json:"siteId"`
	Items   []domain.Item      `json:"items"`
	Options domain.RollOptions `json:"options"`
}

func (a *API) rollStoredVersion(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	var req storedRollRequest
	if !decodeLimit(w, r, &req, maxRollBody) {
		return
	}
	fields := map[string]string{}
	cat := catalog(req.Items, fields)
	opts := a.withDefaults(req.Options, fields)
	key := idempotencyKey(r, fields)
	if !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	for i, it := range req.Items {
		if !ids.Valid(it.ID) {
			fields[fmt.Sprintf("items[%d].id", i)] = "must be a UUID"
		}
	}
	if !validation(w, r, fields) {
		return
	}
	if opts.Mode == "" {
		opts.Mode = domain.ModeMultilevel
	}
	if opts.View == "" {
		opts.View = domain.ViewLegal
	}
	run := domain.MassRoll(cat, opts)
	res, replayed, err := a.store.StoreRoll(r.Context(), store.RollInput{
		VersionID: id, SiteID: req.SiteID, Key: key, Fingerprint: struct {
			ID  string
			Req storedRollRequest
		}{id, req}, Options: opts, Catalog: cat, Run: run,
	})
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	if !replayed {
		a.massRolls.Add(1)
		a.itemsRolled.Add(int64(run.ItemCount))
		a.rollErrors.Add(int64(run.ErrorCount))
	}
	created(w, res, replayed)
}
