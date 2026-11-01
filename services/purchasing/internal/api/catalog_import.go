package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// maxImportBody bounds a vendor import request (the platform default is 4 MiB): a
// 50,000-row file is about 8 MB as CSV text and 20 MB as JSON rows, and the portal's
// whole upload is capped at 25 MB (FR-20.19). A larger body is 413 COM-0413.
const maxImportBody = 32 << 20

// vendorImportBody is POST /catalogs/{id}/vendor-import: the vendor's file as CSV text
// or as JSON rows. The vendor of every row is vendorId.
type vendorImportBody struct {
	VendorID string             `json:"vendorId"`
	Format   string             `json:"format"`
	FileName string             `json:"fileName,omitempty"`
	CSV      string             `json:"csv,omitempty"`
	Rows     []catalogEntryBody `json:"rows,omitempty"`
}

type importDecisionsBody struct {
	AcceptAll bool  `json:"acceptAll,omitempty"`
	Accept    []int `json:"accept,omitempty"`
	Reject    []int `json:"reject,omitempty"`
}

// rawImportRow turns a JSON row into the canonical text columns of a CSV row.
func rawImportRow(e catalogEntryBody) map[string]string {
	raw := map[string]string{
		domain.ColVendorPartNo: e.VendorPartNo, domain.ColProductID: e.ProductID, domain.ColName: e.Name,
		domain.ColDescription: e.Description, domain.ColCategory: e.Category, domain.ColUnit: e.Unit,
		domain.ColUnitPrice: e.UnitPrice, domain.ColCurrency: e.Currency, domain.ColImageURL: e.ImageURL,
	}
	if e.LeadTimeDays != 0 {
		raw[domain.ColLeadTime] = strconv.Itoa(e.LeadTimeDays)
	}
	return raw
}

// stageImportRow validates one file row with the rules of a catalog entry. A failing
// row is ERROR with the reason (PRC-CAT-005); a passing one is ACCEPTABLE.
func stageImportRow(n int, vendorID string, raw map[string]string) domain.ImportRow {
	row := domain.ImportRow{RowNo: n, Raw: map[string]string{}}
	for _, c := range domain.ImportColumns {
		if v := strings.TrimSpace(raw[c]); v != "" {
			row.Raw[c] = v
		}
	}
	reject := func(reason string) domain.ImportRow {
		row.Status, row.Reason = domain.RowError, reason
		return row
	}
	lead, err := domain.LeadTime(row.Raw[domain.ColLeadTime])
	if err != nil {
		return reject("leadTimeDays must be a whole number of days (" + row.Raw[domain.ColLeadTime] + ")")
	}
	entry, err := buildCatalogEntry("row", 0, catalogEntryBody{VendorID: vendorID, VendorPartNo: row.Raw[domain.ColVendorPartNo],
		ProductID: row.Raw[domain.ColProductID], Name: row.Raw[domain.ColName], Description: row.Raw[domain.ColDescription],
		Category: row.Raw[domain.ColCategory], Unit: row.Raw[domain.ColUnit], UnitPrice: row.Raw[domain.ColUnitPrice],
		Currency: row.Raw[domain.ColCurrency], LeadTimeDays: lead, ImageURL: row.Raw[domain.ColImageURL]})
	if err != nil {
		var pr *httpx.Problem
		if errors.As(err, &pr) {
			return reject(strings.ReplaceAll(pr.Detail, "row[0].", ""))
		}
		return reject(err.Error())
	}
	row.Status, row.Entry = domain.RowAcceptable, &entry
	return row
}

// vendorImport handles POST /catalogs/{id}/vendor-import (FR-17.18, AC-17.13): the file
// is staged, every row ACCEPTABLE or ERROR, and nothing reaches the catalog yet.
func (a *API) vendorImport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body vendorImportBody
	if err := httpx.DecodeLimit(r, &body, maxImportBody); err != nil {
		fail(w, r, err)
		return
	}
	if err := uuidField("vendorId", body.VendorID, true); err != nil {
		fail(w, r, err)
		return
	}
	var raws []map[string]string
	switch body.Format {
	case domain.ImportFormatCSV:
		if len(body.Rows) > 0 || strings.TrimSpace(body.CSV) == "" {
			fail(w, r, invalid("csv", "is required for format CSV, and rows must be empty"))
			return
		}
		parsed, perr := domain.ParseImportCSV(body.CSV)
		if perr != nil {
			fail(w, r, importFileProblem(perr))
			return
		}
		raws = parsed
	case domain.ImportFormatJSON:
		if body.CSV != "" || len(body.Rows) == 0 {
			fail(w, r, invalid("rows", "needs at least one row for format JSON, and csv must be empty"))
			return
		}
		if len(body.Rows) > domain.MaxImportRows {
			fail(w, r, importFileProblem(&domain.Error{Code: domain.CodeImportTooBig, Detail: "a vendor import holds at most " + strconv.Itoa(domain.MaxImportRows) + " rows"}))
			return
		}
		for _, e := range body.Rows {
			raws = append(raws, rawImportRow(e))
		}
	default:
		fail(w, r, invalid("format", "must be CSV or JSON"))
		return
	}
	rows := make([]domain.ImportRow, len(raws))
	for i, raw := range raws {
		rows[i] = stageImportRow(i+1, body.VendorID, raw)
	}
	fileName := strings.TrimSpace(body.FileName)
	if len(fileName) > 255 {
		fail(w, r, invalid("fileName", "must be at most 255 characters"))
		return
	}
	out, err := a.store.StageVendorImport(r.Context(), id,
		store.ImportInput{VendorID: body.VendorID, Format: body.Format, FileName: fileName, Rows: rows},
		store.Idem{Key: key, Hash: hashBody("catalog.vendor-import", map[string]any{"catalog": id, "body": body})})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.catalogImportsStaged.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

// importFileProblem maps a whole-file domain error to a 422 problem.
func importFileProblem(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

func (a *API) getCatalogImport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetCatalogImport(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listCatalogImports(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.ImportFilter{CatalogID: q.Get("catalogId"), Status: q.Get("status")}
	if err := uuidField("catalogId", f.CatalogID, false); err != nil {
		fail(w, r, err)
		return
	}
	switch f.Status {
	case "", domain.ImportStaged, domain.ImportCommitted, domain.ImportCancelled:
	default:
		fail(w, r, invalid("status", "must be STAGED, COMMITTED or CANCELLED"))
		return
	}
	page, err := a.store.ListCatalogImports(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// listImportRows handles GET /catalog-imports/{id}/rows; the cursor is the last row number.
func (a *API) listImportRows(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	limit, after, err := numberPageParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	switch status {
	case "", domain.RowAcceptable, domain.RowError, domain.RowAccepted, domain.RowRejected:
	default:
		fail(w, r, invalid("status", "must be ACCEPTABLE, ERROR, ACCEPTED or REJECTED"))
		return
	}
	page, err := a.store.ListImportRows(r.Context(), id, status, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	next := ""
	if page.Next > 0 {
		next = strconv.Itoa(page.Next)
	}
	httpx.JSON(w, http.StatusOK, pageBody[domain.ImportRow]{Items: page.Items, NextCursor: next})
}

// decideImportRows handles POST /catalog-imports/{id}/decisions with If-Match: the
// buyer's accept and reject choices on staged rows.
func (a *API) decideImportRows(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body importDecisionsBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if !body.AcceptAll && len(body.Accept) == 0 && len(body.Reject) == 0 {
		fail(w, r, invalid("accept", "name rows to accept or reject, or set acceptAll"))
		return
	}
	seen := map[int]string{}
	for field, rows := range map[string][]int{"accept": body.Accept, "reject": body.Reject} {
		for _, n := range rows {
			if n < 1 || n > domain.MaxImportRows {
				fail(w, r, invalid(field, "row numbers run from 1 to "+strconv.Itoa(domain.MaxImportRows)))
				return
			}
			if prev, dup := seen[n]; dup {
				fail(w, r, invalid(field, "row "+strconv.Itoa(n)+" is named in "+prev+" and "+field))
				return
			}
			seen[n] = field
		}
	}
	out, err := a.store.DecideImportRows(r.Context(), id, version,
		store.ImportDecisions{AcceptAll: body.AcceptAll, Accept: body.Accept, Reject: body.Reject},
		store.Idem{Key: key, Hash: hashBody("catalog.import.decide", map[string]any{"id": id, "version": version, "body": body})})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// catalogImportAction handles POST /catalog-imports/{id}:commit and :cancel with If-Match
// and an Idempotency-Key.
func (a *API) catalogImportAction(w http.ResponseWriter, r *http.Request) {
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if action != domain.ImportCommit && action != domain.ImportCancel {
		found = false
	}
	if !found || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown catalog import action"))
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.ActOnCatalogImport(r.Context(), id, version, action,
		store.Idem{Key: key, Hash: hashBody("catalog.import."+action, map[string]any{"id": id, "version": version})})
	if err != nil {
		fail(w, r, err)
		return
	}
	if action == domain.ImportCommit {
		a.catalogImportsCommitted.Add(1)
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}
