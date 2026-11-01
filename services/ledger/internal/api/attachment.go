package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// registerAttachments mounts the voucher attachments and notes of journal batches (FR-11.11, FR-11.18).
func (a *API) registerAttachments(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/journal-batches/{id}/attachments", a.listBatchAttachments)
	r.With(authz.RequireDuty(dutyJournalWrite)).Put("/journal-batches/{id}/attachments/{attachmentId}", a.putBatchAttachment)
	r.With(authz.RequireDuty(dutyJournalWrite)).Delete("/journal-batches/{id}/attachments/{attachmentId}", a.deleteBatchAttachment)
	r.With(authz.RequireDuty(dutyRead)).Get("/journal-entries/{id}/attachments", a.listEntryAttachments)
}

type attachmentRequest struct {
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	DocumentType   string `json:"documentType"`
	RetentionClass string `json:"retentionClass"`
	StoreRef       string `json:"storeRef"`
	ContentType    string `json:"contentType"`
	SizeBytes      int64  `json:"sizeBytes"`
	Note           string `json:"note"`
}

func (a *API) listBatchAttachments(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		validationFailed(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	items, err := a.store.ListBatchAttachments(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) listEntryAttachments(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		validationFailed(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	items, err := a.store.ListEntryAttachments(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// putBatchAttachment adds an attachment or note under the client's id: 201 when new, 200 when the
// same content is repeated (idempotent), 409 for other content under the id.
func (a *API) putBatchAttachment(w http.ResponseWriter, r *http.Request) {
	var req attachmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	batchID, id := chi.URLParam(r, "id"), chi.URLParam(r, "attachmentId")
	if fields := attachmentPathErrors(batchID, id); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	v, created, err := a.store.PutBatchAttachment(r.Context(), batchID, id, domain.Attachment{Kind: req.Kind, Name: req.Name,
		DocumentType: req.DocumentType, RetentionClass: req.RetentionClass, StoreRef: req.StoreRef, ContentType: req.ContentType,
		SizeBytes: req.SizeBytes, Note: req.Note})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, v)
}

// attachmentPathErrors names each path parameter that is not a UUID.
func attachmentPathErrors(batchID, id string) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(batchID) {
		fields["id"] = "must be a UUID"
	}
	if !ids.Valid(id) {
		fields["attachmentId"] = "must be a UUID"
	}
	return fields
}

func (a *API) deleteBatchAttachment(w http.ResponseWriter, r *http.Request) {
	batchID, id := chi.URLParam(r, "id"), chi.URLParam(r, "attachmentId")
	if fields := attachmentPathErrors(batchID, id); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if err := a.store.DeleteBatchAttachment(r.Context(), batchID, id, a.now()); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
