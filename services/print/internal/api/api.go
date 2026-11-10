// Package api serves the print service's HTTP routes (api/openapi.yaml).
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
)

const (
	idempotencyKey   = "Idempotency-Key"
	dutyPrint        = "print.job.submit"
	dutyPrintAdmin   = "print.admin"
	validationDetail = "request failed validation"
	readyTimeout     = 2 * time.Second
	defaultJobLimit  = 100
	maxJobLimit      = 500
)

// Sender delivers a rendered document to one printer. The transport package
// implements it; tests use a fake.
type Sender interface {
	Send(ctx context.Context, p domain.Printer, zpl string) error
}

// API groups the handlers over a store and a sender.
type API struct {
	store         store.Store
	sender        Sender
	attempts      int // sends per device before failover (PRINT_RETRY + 1)
	retentionDays int // PRINT_JOB_RETENTION_DAYS

	printed atomic.Int64
	failed  atomic.Int64
	purged  atomic.Int64
}

// Option tunes an API instance. Defaults come from the domain package.
type Option func(*API)

// WithAttemptsPerDevice sets how many sends a device gets before the queue fails over
// (PRINT_RETRY + 1, FR-32.7 and FR-32.13). Values below 1 keep the default.
func WithAttemptsPerDevice(n int) Option {
	return func(a *API) {
		if n >= 1 {
			a.attempts = n
		}
	}
}

// WithRetentionDays sets PRINT_JOB_RETENTION_DAYS, the age at which finished jobs are
// purged. Values below 1 keep the default.
func WithRetentionDays(days int) Option {
	return func(a *API) {
		if days >= 1 {
			a.retentionDays = days
		}
	}
}

// New returns the API.
func New(s store.Store, sender Sender, opts ...Option) *API {
	a := &API{store: s, sender: sender, attempts: domain.FailoverAttempts, retentionDays: domain.DefaultRetentionDays}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Register mounts the /print/v1 routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.Route("/print/v1", func(r chi.Router) {
		r.With(authz.RequireDuty(dutyPrintAdmin)).Post("/printers", a.createPrinter)
		r.With(authz.RequireDuty(dutyPrint)).Get("/printers", a.listPrinters)
		r.With(authz.RequireDuty(dutyPrint)).Get("/printers/status", a.printerStatuses)
		r.With(authz.RequireDuty(dutyPrintAdmin)).Post("/routing-rules", a.createRoutingRule)
		r.With(authz.RequireDuty(dutyPrint)).Get("/routing-rules", a.listRoutingRules)
		r.With(authz.RequireDuty(dutyPrint)).Post("/jobs", a.createJob)
		r.With(authz.RequireDuty(dutyPrint)).Get("/jobs", a.listJobs)
		r.With(authz.RequireDuty(dutyPrintAdmin)).Post("/jobs:purge", a.purgeJobs)
		r.With(authz.RequireDuty(dutyPrint)).Get("/jobs/{id}", a.getJob)
		r.With(authz.RequireDuty(dutyRenderDocument)).Post("/documents:render", a.renderDocument)
	})
}

type printerRequest struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	DeviceType   string `json:"deviceType"`
	Dpi          int    `json:"dpi"`
	MediaSize    string `json:"mediaSize"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	QueueCode    string `json:"queueCode"`
	FailoverRank int    `json:"failoverRank"`
}

// createJobRequest names the queue directly, or a documentType (and optional
// zone/warehouseId) that the routing rules resolve to a queue (BR-32.12).
// A label job renders templateCode with data; a PDF job (format PDF) carries the
// document in documentBase64 instead.
type createJobRequest struct {
	TemplateCode   string            `json:"templateCode"`
	Format         string            `json:"format,omitempty"`
	DocumentBase64 string            `json:"documentBase64,omitempty"`
	QueueCode      string            `json:"queueCode,omitempty"`
	DocumentType   string            `json:"documentType,omitempty"`
	Zone           string            `json:"zone,omitempty"`
	WarehouseID    string            `json:"warehouseId,omitempty"`
	Data           map[string]string `json:"data"`
	ReprintOf      *string           `json:"reprintOf,omitempty"`
}

// pdfDocument checks the PDF fields of a job request and returns the decoded
// document. A PDF job has no template and no label data.
func pdfDocument(req createJobRequest, fields map[string]string) []byte {
	if req.TemplateCode != "" {
		fields["templateCode"] = "must be empty for a PDF document"
	}
	if len(req.Data) > 0 {
		fields["data"] = "must be empty for a PDF document"
	}
	if req.DocumentBase64 == "" {
		fields["documentBase64"] = "is required for a PDF document"
		return nil
	}
	doc, err := base64.StdEncoding.DecodeString(req.DocumentBase64)
	switch {
	case err != nil:
		fields["documentBase64"] = "must be standard base64"
	case len(doc) > domain.MaxDocumentBytes:
		fields["documentBase64"] = "must decode to at most 2 MiB"
	case !bytes.HasPrefix(doc, []byte("%PDF-")):
		fields["documentBase64"] = "must be a PDF document (starting with %PDF-)"
	}
	return doc
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return tenant.Principal{}, false
	}
	return p, true
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func requireKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(idempotencyKey)
	if key == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "the Idempotency-Key header is required"))
		return "", false
	}
	return key, true
}

func (a *API) createPrinter(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req printerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.Printer{Code: req.Code, Name: req.Name, DeviceType: req.DeviceType, Dpi: req.Dpi, MediaSize: req.MediaSize,
		Host: req.Host, Port: req.Port, QueueCode: req.QueueCode, FailoverRank: req.FailoverRank}
	if in.Port == 0 {
		in.Port = domain.DefaultPort(in.DeviceType)
	}
	if in.MediaSize == "" {
		in.MediaSize = domain.DefaultMedia(in.DeviceType)
	}
	if fields := in.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, _, err := a.store.CreatePrinter(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listPrinters(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListPrinters(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []domain.Printer `json:"items"`
	}{Items: items})
}

// problemFrom maps a domain rule error to a 400 problem.
func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusBadRequest, de.Code, de.Detail)
	}
	return err
}

// requestHash fingerprints the request body so a reused Idempotency-Key with a
// different body is refused.
func requestHash(req createJobRequest) string {
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a *API) createJob(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createJobRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	switch {
	case req.QueueCode == "" && req.DocumentType == "":
		fields["queueCode"] = "queueCode or documentType is required"
	case req.QueueCode != "" && !domain.ValidCode(req.QueueCode):
		fields["queueCode"] = "must be a valid queue code"
	}
	if req.DocumentType != "" && !domain.ValidDocumentType(req.DocumentType) {
		fields["documentType"] = "must be a routable document type"
	}
	if req.ReprintOf != nil && !ids.Valid(*req.ReprintOf) {
		fields["reprintOf"] = "must be a UUID"
	}
	format := domain.FormatZPL
	var doc []byte
	switch {
	case req.Format == domain.FormatPDF && req.ReprintOf != nil:
		fields["format"] = "must be omitted on a reprint, which reuses the original job's format"
	case req.Format == domain.FormatPDF:
		format = domain.FormatPDF
		doc = pdfDocument(req, fields)
	case req.Format != "" && req.Format != domain.FormatZPL:
		fields["format"] = "must be ZPL or PDF"
	case req.DocumentBase64 != "":
		fields["documentBase64"] = "is only allowed with format PDF"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	var zpl, template string
	// labelData is the template data of a new label job, so a 1 in device can get
	// its compact layout (FR-32.7). A reprint has none and reuses the stored payload.
	var labelData map[string]string
	if req.ReprintOf != nil {
		// BR-32.8: a reprint reuses the stored payload and makes a new job with reprint_of.
		orig, err := a.store.GetJob(r.Context(), *req.ReprintOf)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		zpl, template, format = orig.ZPL, orig.TemplateCode, orig.Format
	} else if format == domain.FormatPDF {
		zpl, template = string(doc), domain.PDFDocumentTemplate
	} else {
		tpl, known := domain.Template(req.TemplateCode)
		if !known {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeUnknownTemplate, "template "+req.TemplateCode+" is not built in"))
			return
		}
		// ITEM_LABEL and GS1_CARTON_LABEL compose their GS1-128 element string from the fields.
		data, err := domain.PrepareLabelData(req.TemplateCode, req.Data)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		rendered, err := domain.Render(tpl, data)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		zpl, template, labelData = rendered, req.TemplateCode, data
	}
	queue := req.QueueCode
	if queue == "" {
		rules, err := a.store.ListRoutingRules(r.Context())
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		rule, ok := domain.ResolveRoute(rules, domain.Document{Type: req.DocumentType, UserID: p.UserID, Zone: req.Zone, WarehouseID: req.WarehouseID})
		if !ok {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNoPrinter, "no printer is routed for "+req.DocumentType))
			return
		}
		queue = rule.QueueCode
	}
	job := domain.Job{ID: ids.New(), TemplateCode: template, QueueCode: queue, Format: format, Status: domain.JobQueued,
		ReprintOf: req.ReprintOf, CreatedBy: p.UserID, ZPL: zpl}
	created, inserted, err := a.store.CreateJob(r.Context(), store.NewJob{Job: job, RequestHash: requestHash(req), IdempotencyKey: key})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !inserted {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, created)
		return
	}
	// The job is stored: finish dispatch and record the outcome even if the client
	// goes away, so it is not left QUEUED (the printer may already hold the label).
	ctx := context.WithoutCancel(r.Context())
	devices, err := a.store.QueueDevices(ctx, queue)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// A ZPL label goes to ZPL_II devices and a PDF document to PDF_IPP devices; the
	// rest of the queue is skipped, and a queue with none fails with MOB-0112.
	// FR-32.7: a ZPL label is laid out and sized for each device's media (the
	// compact layout on 2 x 1 and 3 x 1 in); a label that does not fit a device
	// counts as a failed attempt there, so the queue fails over.
	out, dispatchErr := domain.Dispatch(domain.Accepting(devices, domain.DeviceTypeFor(format)), a.attempts, func(d domain.Printer) error {
		label, err := domain.LabelForDevice(d, template, zpl, labelData)
		if err != nil {
			return err
		}
		payload, err := domain.SizeForDevice(format, d, label)
		if err != nil {
			return err
		}
		return a.sender.Send(ctx, d, payload)
	})
	if err := created.Complete(out, dispatchErr); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.store.CompleteJob(ctx, created); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if created.Status == domain.JobPrinted {
		a.printed.Add(1)
	} else {
		a.failed.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, created)
}

var jobStatuses = map[string]bool{domain.JobQueued: true, domain.JobPrinted: true, domain.JobFailed: true}

// listJobs is the print job log (§6.32.14): newest first, filtered by status,
// queue or printer.
func (a *API) listJobs(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	f := store.JobFilter{Status: q.Get("status"), QueueCode: q.Get("queueCode"), PrinterCode: q.Get("printerCode"), Limit: defaultJobLimit}
	fields := map[string]string{}
	if f.Status != "" && !jobStatuses[f.Status] {
		fields["status"] = "must be QUEUED, PRINTED or FAILED"
	}
	if f.QueueCode != "" && !domain.ValidCode(f.QueueCode) {
		fields["queueCode"] = "must be a valid queue code"
	}
	if f.PrinterCode != "" && !domain.ValidCode(f.PrinterCode) {
		fields["printerCode"] = "must be a valid printer code"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxJobLimit {
			fields["limit"] = "must be an integer from 1 to 500"
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	items, err := a.store.ListJobs(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []domain.Job `json:"items"`
	}{Items: items})
}

// ready reports 503 while the database is unreachable, so the pod leaves the Service.
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) getJob(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeJobNotFound, "print job not found"))
		return
	}
	job, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, job)
}
