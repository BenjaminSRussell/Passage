package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type documentTypeBody struct {
	ID                 string `json:"id,omitempty"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	ChangeControlled   bool   `json:"changeControlled"`
	SignatureOnRelease bool   `json:"signatureOnRelease"`
	IndependentReview  bool   `json:"independentReview"`
	SignatureMeaning   string `json:"signatureMeaning,omitempty"`
}

func bodyFromDocumentType(t domain.DocumentType) documentTypeBody {
	return documentTypeBody{ID: t.ID, Code: t.Code, Name: t.Name, ChangeControlled: t.ChangeControlled, SignatureOnRelease: t.SignatureOnRelease, IndependentReview: t.IndependentReview, SignatureMeaning: t.SignatureMeaning}
}

func (a *API) createDocumentType(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body documentTypeBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	validID("code", body.Code, fields)
	if n := len(body.Name); n == 0 || n > 200 {
		fields["name"] = "must be 1 to 200 characters"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateDocumentType(r.Context(), domain.DocumentType{
		Code: body.Code, Name: body.Name, ChangeControlled: body.ChangeControlled, SignatureOnRelease: body.SignatureOnRelease,
		IndependentReview: body.IndependentReview, SignatureMeaning: body.SignatureMeaning, Network: domain.DefaultStatusNetwork(),
	}, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create document type", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromDocumentType(created))
}

func (a *API) listDocumentTypes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	listPage(a, w, r, "list document types", &a.bomFailed, a.store.ListDocumentTypes, bodyFromDocumentType)
}

func (a *API) getDocumentType(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "code")
	if code == "" || len(code) > maxIDLength {
		invalid(w, r, map[string]string{"code": "must be 1 to 64 characters"})
		return
	}
	t, err := a.store.GetDocumentType(r.Context(), code)
	if err != nil {
		a.fail(w, r, "get document type", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromDocumentType(t))
}

type originalBody struct {
	StorageKey string `json:"storageKey"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
}

type linkBody struct {
	TargetType string `json:"targetType"`
	TargetID   string `json:"targetId"`
}

type documentBody struct {
	ID          string         `json:"id,omitempty"`
	TypeCode    string         `json:"typeCode"`
	Number      string         `json:"number"`
	Part        string         `json:"part,omitempty"`
	Version     string         `json:"version,omitempty"`
	Description string         `json:"description,omitempty"`
	Status      string         `json:"status,omitempty"`
	LastEditor  string         `json:"lastEditor,omitempty"`
	EcoID       string         `json:"ecoId,omitempty"`
	SignedAt    string         `json:"signedAt,omitempty"`
	Originals   []originalBody `json:"originals,omitempty"`
	Links       []linkBody     `json:"links,omitempty"`
}

func bodyFromDocument(d domain.Document) documentBody {
	body := documentBody{ID: d.ID, TypeCode: d.TypeCode, Number: d.Number, Part: d.Part, Version: d.Version, Description: d.Description, Status: d.Status, LastEditor: d.LastEditor, EcoID: d.EcoID, Originals: make([]originalBody, len(d.Originals)), Links: make([]linkBody, len(d.Links))}
	if !d.SignedAt.IsZero() {
		body.SignedAt = d.SignedAt.UTC().Format(dateLayout + "T15:04:05Z")
	}
	for i, o := range d.Originals {
		body.Originals[i] = originalBody{StorageKey: o.StorageKey, SHA256: o.SHA256, SizeBytes: o.SizeBytes}
	}
	for i, l := range d.Links {
		body.Links[i] = linkBody{TargetType: l.TargetType, TargetID: l.TargetID}
	}
	return body
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validOriginals(tenantID string, originals []originalBody, fields map[string]string) {
	if len(originals) > maxOriginals {
		fields["originals"] = fmt.Sprintf("must list at most %d files", maxOriginals)
		return
	}
	seen := map[string]bool{}
	for i, o := range originals {
		field := fmt.Sprintf("originals[%d]", i)
		switch {
		case !strings.HasPrefix(o.StorageKey, tenantID+"/") || len(o.StorageKey) > maxStorageKey || strings.Contains(o.StorageKey, ".."):
			fields[field] = "storageKey must start with the tenant id and a slash and must not contain .."
		case !sha256Hex.MatchString(o.SHA256):
			fields[field] = "sha256 must be 64 lowercase hex characters"
		case o.SizeBytes < 0:
			fields[field] = "sizeBytes must not be negative"
		case seen[o.StorageKey]:
			fields[field] = "storageKey is listed twice"
		}
		seen[o.StorageKey] = true
	}
}

func (a *API) createDocument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body documentBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	fields := map[string]string{}
	validID("typeCode", body.TypeCode, fields)
	validID("number", body.Number, fields)
	if body.EcoID != "" {
		fields["ecoId"] = "is set by the ECO that replaces the document, not on create"
	}
	validOriginals(p.TenantID, body.Originals, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if _, err := a.store.GetDocumentType(r.Context(), body.TypeCode); err != nil {
		a.fail(w, r, "create document", err, &a.bomFailed)
		return
	}
	doc := domain.Document{TypeCode: body.TypeCode, Number: body.Number, Part: body.Part, Version: body.Version, Description: body.Description, LastEditor: p.UserID}
	for _, o := range body.Originals {
		doc.Originals = append(doc.Originals, domain.DocumentOriginal{StorageKey: o.StorageKey, SHA256: o.SHA256, SizeBytes: o.SizeBytes})
	}
	for _, l := range body.Links {
		doc.Links = append(doc.Links, domain.DocumentLink{TargetType: l.TargetType, TargetID: l.TargetID})
	}
	created, err := a.store.CreateDocument(r.Context(), doc, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create document", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromDocument(created))
}

func (a *API) listDocuments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	number := r.URL.Query().Get("number")
	listPage(a, w, r, "list documents", &a.bomFailed, func(ctx context.Context, after string, limit int) (store.Page[domain.Document], error) {
		return a.store.ListDocuments(ctx, number, after, limit)
	}, bodyFromDocument)
}

func (a *API) getDocument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := a.store.GetDocument(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get document", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromDocument(d))
}

func (a *API) transitionDocument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if body.To == "" {
		invalid(w, r, map[string]string{"to": "is required"})
		return
	}
	p, _ := tenant.FromContext(r.Context())
	doc, err := a.store.TransitionDocument(r.Context(), id, body.To, p.UserID, nil)
	if err != nil {
		a.fail(w, r, "transition document", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromDocument(doc))
}

func (a *API) signDocument(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "signing a document needs the acting user"))
		return
	}
	var body struct {
		PrintedName string `json:"printedName"`
		Method      string `json:"method"`
		To          string `json:"to"`
	}
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &body); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	to := body.To
	if to == "" {
		to = domain.DocReleased
	}
	doc, err := a.store.GetDocument(r.Context(), id)
	if err != nil {
		a.fail(w, r, "sign document", err, &a.bomFailed)
		return
	}
	typ, err := a.store.GetDocumentType(r.Context(), doc.TypeCode)
	if err != nil {
		a.fail(w, r, "sign document", err, &a.bomFailed)
		return
	}
	sig, err := domain.SignDocument(typ, doc, to, body.Method, p.UserID, body.PrintedName, a.now())
	if err != nil {
		a.fail(w, r, "sign document", err, &a.bomFailed)
		return
	}
	auth := domain.Reauth{UserID: p.UserID, Assertion: r.Header.Get(reauthHeader), Method: body.Method, DocumentID: doc.ID, RecordHash: sig.OriginalsHash}
	if err := a.reauth.Verify(r.Context(), p.UserID, auth); err != nil {
		a.fail(w, r, "reauth", err, &a.bomFailed)
		return
	}
	signed, err := a.store.TransitionDocument(r.Context(), id, to, p.UserID, &sig)
	if err != nil {
		a.fail(w, r, "sign document", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromDocument(signed))
}

type signatureBody struct {
	ID              string `json:"id"`
	RecordID        string `json:"recordId"`
	DocumentNumber  string `json:"documentNumber"`
	DocumentVersion string `json:"documentVersion"`
	Transition      string `json:"transition"`
	SignerID        string `json:"signerId"`
	PrintedName     string `json:"printedName"`
	Meaning         string `json:"meaning"`
	SignedAt        string `json:"signedAt"`
	OriginalsHash   string `json:"originalsHash"`
	Method          string `json:"method,omitempty"`
}

func bodyFromSignature(s domain.Signature) signatureBody {
	return signatureBody{ID: s.ID, RecordID: s.RecordID, DocumentNumber: s.DocumentNumber, DocumentVersion: s.DocumentVersion, Transition: s.Transition, SignerID: s.SignerID, PrintedName: s.PrintedName, Meaning: s.Meaning, SignedAt: s.SignedAt.UTC().Format("2006-01-02T15:04:05Z"), OriginalsHash: s.OriginalsHash, Method: s.Method}
}

func (a *API) listSignatures(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	sigs, err := a.store.ListSignatures(r.Context(), id)
	if err != nil {
		a.fail(w, r, "list signatures", err, &a.bomFailed)
		return
	}
	items := make([]signatureBody, len(sigs))
	for i, s := range sigs {
		items[i] = bodyFromSignature(s)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) documentWhereUsed(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := a.store.DocumentWhereUsed(r.Context(), id)
	if err != nil {
		a.fail(w, r, "document where-used", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) setDocumentStructure(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Children []string `json:"children"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(body.Children) > maxDocumentChildren {
		fields["children"] = fmt.Sprintf("must list at most %d documents", maxDocumentChildren)
	}
	seen := map[string]bool{}
	for i, child := range body.Children {
		field := fmt.Sprintf("children[%d]", i)
		if !ids.Valid(child) {
			fields[field] = "must be a UUID"
		} else if seen[child] {
			fields[field] = "is listed twice"
		}
		seen[child] = true
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if err := a.store.SetDocumentStructure(r.Context(), id, body.Children); err != nil {
		a.fail(w, r, "set document structure", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}
