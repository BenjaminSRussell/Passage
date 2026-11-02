package api

import (
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// DutyBatchRecordWrite carries the PRD's *Review batch records* (4.24.17: EBR
// review stage and exception disposition) plus the record setup: profiles,
// compiling a record, raising an exception by hand and deciding one. Approving
// and sealing stay under DutyBatchRecordApprove (*Approve batch records*), and
// the store refuses an approval by anyone who reviewed the same record
// (PMF-0124). The duty id is not in OPA data yet; the service enforces it and
// the policy data owner adds it (see NOT-IMPLEMENTED.md).
const DutyBatchRecordWrite = "production.batch-record.write"

const (
	profilesPath = "/v1/production/batch-record-profiles"
	recordsPath  = "/v1/production/batch-records"

	maxExceptionText = 500
	maxCommentText   = 500
)

var profileCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,39}$`)

// registerRecords mounts the electronic batch record: its profiles, the
// compiled record, review by exception, approval and the seal (FR-24.21,
// BR-24.15, AC-24.16).
func (a *API) registerRecords(r chi.Router) {
	write := authz.RequireDuty(DutyBatchRecordWrite)
	approve := authz.RequireDuty(DutyBatchRecordApprove)
	read := authz.RequireDuty(DutyRead)
	r.With(write).Post(profilesPath, a.defineProfile)
	r.With(read).Get(profilesPath, a.listProfiles)
	r.With(read).Get(profilesPath+"/{id}", a.getProfile)
	r.With(write).Post(profilesPath+"/{id}:retire", a.retireProfile)
	r.With(write).Post(recordsPath, a.compileRecord)
	r.With(read).Get(recordsPath, a.listRecords)
	r.With(read).Get(recordsPath+"/{id}", a.getRecord)
	r.With(write).Post(recordsPath+"/{id}/exceptions", a.raiseException)
	r.With(write).Post(recordsPath+"/{id}/exceptions/{exceptionId}:disposition", a.dispositionException)
	r.With(approve).Post(recordsPath+"/{id}:approve", a.approveRecord)
	r.With(approve).Post(recordsPath+"/{id}:seal", a.sealRecord)
}

type profileRequest struct {
	LegalEntityID              string `json:"legalEntityId"`
	Code                       string `json:"code"`
	ItemID                     string `json:"itemId"`
	ApprovalRequiredForRelease *bool  `json:"approvalRequiredForRelease"`
}

func (a *API) defineProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req profileRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	}
	if !ids.Valid(req.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	if !profileCode.MatchString(req.Code) {
		fields["code"] = "must be 1 to 40 characters: upper-case letters, digits, '_', '.' or '-'"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+req.LegalEntityID+" is outside your scope"))
		return
	}
	required := req.ApprovalRequiredForRelease == nil || *req.ApprovalRequiredForRelease
	out, fresh, err := a.store.DefineRecordProfile(r.Context(), store.RecordProfile{LegalEntityID: req.LegalEntityID, Code: req.Code,
		ItemID: req.ItemID, ApprovalRequiredForRelease: required}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, out)
}

func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f := store.ProfileFilter{Limit: limit, ActiveOnly: r.URL.Query().Get("active") == "true"}
	if v := r.URL.Query().Get("itemId"); v != "" {
		c, ok := ids.Canonical(v)
		if !ok {
			invalid(w, r, map[string]string{"itemId": "must be a UUID"})
			return
		}
		f.ItemID = c
	}
	items, err := a.store.ListRecordProfiles(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetRecordProfile(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// versionCommand reads the body of a command that only carries the version the
// caller read.
func versionCommand(w http.ResponseWriter, r *http.Request) (id string, version int, idem store.Idem, ok bool) {
	if _, ok = principal(w, r); !ok {
		return
	}
	if id, ok = pathID(w, r); !ok {
		return
	}
	var req retireRequest
	if idem, ok = decodeCommand(w, r, &req); !ok {
		return
	}
	if req.Version < 1 {
		invalid(w, r, map[string]string{"version": "must be the version you read (1 or more)"})
		return "", 0, idem, false
	}
	return id, req.Version, idem, true
}

func (a *API) retireProfile(w http.ResponseWriter, r *http.Request) {
	id, version, idem, ok := versionCommand(w, r)
	if !ok {
		return
	}
	out, fresh, err := a.store.RetireRecordProfile(r.Context(), id, version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, out)
}

type compileRequest struct {
	BatchID string `json:"batchId"`
}

func (a *API) compileRecord(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req compileRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	batch, ok := ids.Canonical(req.BatchID)
	if !ok {
		invalid(w, r, map[string]string{"batchId": "must be a UUID"})
		return
	}
	out, fresh, err := a.store.CompileRecord(r.Context(), batch, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, out)
}

func (a *API) listRecords(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.RecordFilter{Status: q.Get("status"), Limit: limit}
	fields := map[string]string{}
	switch f.Status {
	case "", store.RecordInReview, store.RecordApproved, store.RecordSealed:
	default:
		fields["status"] = "must be in-review, approved or sealed"
	}
	for name, dst := range map[string]*string{"batchId": &f.BatchID, "itemId": &f.ItemID} {
		if v := q.Get(name); v != "" {
			c, ok := ids.Canonical(v)
			if !ok {
				fields[name] = "must be a UUID"
			}
			*dst = c
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListRecords(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRecord(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetRecord(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type raiseExceptionRequest struct {
	Version     int    `json:"version"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

func (a *API) raiseException(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req raiseExceptionRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	if req.Severity == "" {
		req.Severity = "major"
	}
	req.Description = strings.TrimSpace(req.Description)
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if n := utf8.RuneCountInString(req.Description); n < 1 || n > maxExceptionText {
		fields["description"] = "must be 1 to 500 characters"
	}
	if !domain.ValidSeverity(req.Severity) {
		fields["severity"] = "must be minor, major or critical"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	out, fresh, err := a.store.RaiseException(r.Context(), id, store.NewException{Description: req.Description, Severity: req.Severity}, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, out)
}

type dispositionExceptionRequest struct {
	Version     int    `json:"version"`
	Disposition string `json:"disposition"`
	Comment     string `json:"comment"`
	SignatureID string `json:"signatureId"`
}

func (a *API) dispositionException(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	exceptionID := chi.URLParam(r, "exceptionId")
	if !ids.Valid(exceptionID) {
		invalid(w, r, map[string]string{"exceptionId": "must be a UUID"})
		return
	}
	var req dispositionExceptionRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	req.Comment = strings.TrimSpace(req.Comment)
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if !domain.ValidExceptionDisposition(req.Disposition) {
		fields["disposition"] = "must be accepted, deviation-raised or rejected"
	}
	sig, sigOK := ids.Canonical(req.SignatureID)
	if !sigOK {
		fields["signatureId"] = "must be the UUID of the signature that backs the decision"
	}
	if utf8.RuneCountInString(req.Comment) > maxCommentText {
		fields["comment"] = "at most 500 characters"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	out, fresh, err := a.store.DispositionException(r.Context(), id, strings.ToLower(exceptionID),
		store.ExceptionDisposition{Disposition: req.Disposition, Comment: req.Comment, SignatureID: sig}, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, out)
}

func (a *API) approveRecord(w http.ResponseWriter, r *http.Request) {
	id, version, idem, ok := versionCommand(w, r)
	if !ok {
		return
	}
	out, fresh, err := a.store.ApproveRecord(r.Context(), id, version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, out)
}

func (a *API) sealRecord(w http.ResponseWriter, r *http.Request) {
	id, version, idem, ok := versionCommand(w, r)
	if !ok {
		return
	}
	out, fresh, err := a.store.SealRecord(r.Context(), id, version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, out)
}
