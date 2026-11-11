package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// renderRequest is a document plus what docgen adds: a template (by code, else the default
// template of the document type), the language of the text blocks, the text block that is
// the letter body and the values of their placeholders.
type renderRequest struct {
	domain.DocumentRequest
	TemplateCode string            `json:"templateCode,omitempty"`
	Language     string            `json:"language,omitempty"`
	BodyBlock    string            `json:"bodyBlock,omitempty"`
	Values       map[string]string `json:"values,omitempty"`
}

// domainProblem turns a domain rule violation into an RFC 9457 problem: 404 for a missing
// template or text block, 422 for the rest. Other errors pass through.
func domainProblem(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusUnprocessableEntity
	if de.Code == domain.CodeTemplateNotFound || de.Code == domain.CodeTextBlockNotFound {
		status = http.StatusNotFound
	}
	return httpx.NewProblem(status, de.Code, de.Detail)
}

// compose applies the template and text blocks of a render request. Without a docgen store
// or a template and text blocks, the document is rendered as sent.
func (a *API) compose(r *http.Request, req renderRequest) (domain.DocumentRequest, error) {
	if req.TemplateCode == "" && req.BodyBlock == "" && a.docs == nil {
		return req.DocumentRequest, nil
	}
	if a.docs == nil {
		return req.DocumentRequest, httpx.NewProblem(http.StatusServiceUnavailable, "PRN-0016", "templates and text blocks are not available")
	}
	ctx := r.Context()
	var tpl *domain.DocumentTemplate
	if req.TemplateCode != "" {
		t, err := a.docs.GetTemplate(ctx, req.TemplateCode)
		if err != nil {
			return req.DocumentRequest, err
		}
		tpl = &t
	} else {
		t, err := a.docs.DefaultTemplate(ctx, req.DocumentType)
		if err != nil {
			return req.DocumentRequest, err
		}
		tpl = t
	}
	in := domain.ComposeInput{Language: req.Language, BodyBlock: req.BodyBlock, Values: req.Values}
	return domain.Compose(req.DocumentRequest, tpl, in, func(code string) ([]domain.TextBlock, error) {
		return a.docs.TextBlockVersions(ctx, code)
	})
}

func (a *API) registerDocgen(r chi.Router) {
	admin, read := authz.RequireDuty(dutyPrintAdmin), authz.RequireDuty(dutyPrint)
	r.With(admin).Put("/document-templates/{code}", a.putTemplate)
	r.With(read).Get("/document-templates", a.listTemplates)
	r.With(read).Get("/document-templates/{code}", a.getTemplate)
	r.With(admin).Put("/text-blocks/{code}/{language}", a.putTextBlock)
	r.With(read).Get("/text-blocks", a.listTextBlocks)
	r.With(admin).Post("/output-rules", a.createOutputRule)
	r.With(admin).Delete("/output-rules/{id}", a.deleteOutputRule)
	r.With(read).Get("/output-rules", a.listOutputRules)
	r.With(read).Get("/output-plan", a.outputPlan)
}

// canonicalID lowercases a UUID; anything else is returned as it came, for validation to refuse.
func canonicalID(s string) string {
	if c, ok := ids.Canonical(s); ok {
		return c
	}
	return s
}

// docgenStore returns the docgen store, or answers 503 when there is none.
func (a *API) docgenStore(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	if _, ok := principal(w, r); !ok {
		return nil, false
	}
	if a.docs == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "PRN-0016", "templates, text blocks and output rules are not available"))
		return nil, false
	}
	return r.Context(), true
}

type templateRequest struct {
	Name         string                `json:"name"`
	DocumentType string                `json:"documentType"`
	Default      bool                  `json:"default"`
	Config       domain.TemplateConfig `json:"config"`
}

// putTemplate creates or replaces a document template (46-1). Every save raises its version.
func (a *API) putTemplate(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	var req templateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.DocumentTemplate{Code: chi.URLParam(r, "code"), Name: req.Name, DocumentType: req.DocumentType, Default: req.Default, Config: req.Config}
	if fields := in.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	blocks := func(code string) ([]domain.TextBlock, error) { return a.docs.TextBlockVersions(ctx, code) }
	problems, err := domain.CheckTemplateBlocks(in, blocks)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(problems) > 0 {
		validationFailed(w, r, problems)
		return
	}
	out, err := a.docs.PutTemplate(ctx, in)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getTemplate(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	out, err := a.docs.GetTemplate(ctx, chi.URLParam(r, "code"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	items, err := a.docs.ListTemplates(ctx, r.URL.Query().Get("documentType"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []domain.DocumentTemplate `json:"items"`
	}{Items: items})
}

type textBlockRequest struct {
	Body string `json:"body"`
}

// putTextBlock creates or replaces one language version of a standard text (46-4).
func (a *API) putTextBlock(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	var req textBlockRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.TextBlock{Code: chi.URLParam(r, "code"), Language: chi.URLParam(r, "language"), Body: req.Body}
	if fields := in.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	all, err := a.docs.ListTemplates(ctx, "")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if why := domain.DefaultTemplateBlockProblem(all, in.Code, in.Body); why != "" {
		validationFailed(w, r, map[string]string{"body": why})
		return
	}
	out, err := a.docs.PutTextBlock(ctx, in)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listTextBlocks(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	items, err := a.docs.ListTextBlocks(ctx)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []domain.TextBlock `json:"items"`
	}{Items: items})
}

type outputRuleRequest struct {
	DocumentType  string `json:"documentType"`
	LegalEntityID string `json:"legalEntityId,omitempty"`
	AccountID     string `json:"accountId,omitempty"`
	CopyKind      string `json:"copyKind"`
	Channel       string `json:"channel"`
	Destination   string `json:"destination,omitempty"`
	Copies        int    `json:"copies"`
	Sequence      int    `json:"sequence"`
}

// createOutputRule adds an output rule (46-2). Idempotency-Key is required.
func (a *API) createOutputRule(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req outputRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Copies == 0 {
		req.Copies = 1
	}
	in := domain.OutputRule{DocumentType: req.DocumentType, LegalEntityID: canonicalID(req.LegalEntityID), AccountID: canonicalID(req.AccountID), CopyKind: req.CopyKind,
		Channel: req.Channel, Destination: req.Destination, Copies: req.Copies, Sequence: req.Sequence}
	if fields := in.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// A rule of one legal entity needs that entity in the caller's scope; a rule for every
	// legal entity needs tenant-wide authority (no entity claim, tenant.administer, print.admin).
	p, _ := tenant.FromContext(ctx)
	if in.LegalEntityID != "" {
		if !tenant.InLegalEntityScope(p, in.LegalEntityID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope"))
			return
		}
	} else if !dbx.TenantWide(p, dutyPrintAdmin) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "only a tenant administrator can add a rule that applies to every legal entity"))
		return
	}
	out, _, err := a.docs.CreateOutputRule(ctx, in, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listOutputRules(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	items, err := a.docs.ListOutputRules(ctx)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []domain.OutputRule `json:"items"`
	}{Items: items})
}

func (a *API) deleteOutputRule(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodeOutputRuleNotFound, "output rule")
	if !ok {
		return
	}
	p, _ := tenant.FromContext(ctx)
	if err := a.docs.DeleteOutputRule(ctx, id, dbx.TenantWide(p, dutyPrintAdmin)); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// outputPlan answers where the original and the copies of one document go (46-2): the
// output rules that match its type, legal entity and account, most specific scope first.
func (a *API) outputPlan(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	doc := domain.OutputDocument{Type: q.Get("documentType"), LegalEntityID: canonicalID(q.Get("legalEntityId")), AccountID: canonicalID(q.Get("accountId"))}
	known := false
	for _, t := range domain.DocumentTypes() {
		known = known || t == doc.Type
	}
	if !known {
		validationFailed(w, r, map[string]string{"documentType": "must be a document type the renderer knows"})
		return
	}
	rules, err := a.docs.ListOutputRules(ctx)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []domain.OutputRule `json:"items"`
	}{Items: domain.ResolveOutputs(rules, doc)})
}
