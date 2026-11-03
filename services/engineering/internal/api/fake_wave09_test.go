package api_test

import (
	"context"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type wave09State struct {
	org       domain.EngineeringOrganization
	nomen     domain.Nomenclature
	cat       domain.EngineeringCategory
	version   domain.EngineeringVersion
	policy    domain.ReleasePolicy
	session   store.ReleaseSessionRecord
	line      domain.ReleaseLine
	readiness domain.ReadinessPolicy
	released  store.ReleasedProduct
	docType   domain.DocumentType
	doc       domain.Document
	sigs      []domain.Signature
	ebom      store.EbomImportRecord
}

func (f *fakeStore) w() *wave09State {
	if f.wave == nil {
		f.wave = &wave09State{}
	}
	return f.wave
}

func withID(id string) string {
	if id == "" {
		return ids.New()
	}
	return id
}

func (f *fakeStore) CreateOrganization(_ context.Context, o domain.EngineeringOrganization, _ store.Idem) (domain.EngineeringOrganization, error) {
	o.ID = withID(o.ID)
	f.w().org = o
	return o, f.err
}
func (f *fakeStore) GetOrganization(context.Context, string) (domain.EngineeringOrganization, error) {
	return f.w().org, f.err
}
func (f *fakeStore) ListOrganizations(context.Context, string, int) (store.Page[domain.EngineeringOrganization], error) {
	var items []domain.EngineeringOrganization
	if f.w().org.ID != "" {
		items = []domain.EngineeringOrganization{f.w().org}
	}
	return store.Page[domain.EngineeringOrganization]{Items: items}, f.err
}
func (f *fakeStore) CreateNomenclature(_ context.Context, n domain.Nomenclature, _ store.Idem) (domain.Nomenclature, error) {
	n.ID = withID(n.ID)
	f.w().nomen = n
	return n, f.err
}
func (f *fakeStore) GetNomenclature(context.Context, string) (domain.Nomenclature, error) {
	return f.w().nomen, f.err
}
func (f *fakeStore) ListNomenclatures(context.Context, string, int) (store.Page[domain.Nomenclature], error) {
	return store.Page[domain.Nomenclature]{}, f.err
}
func (f *fakeStore) CreateCategory(_ context.Context, c domain.EngineeringCategory, _ store.Idem) (domain.EngineeringCategory, error) {
	c.ID = withID(c.ID)
	f.w().cat = c
	return c, f.err
}
func (f *fakeStore) GetCategory(context.Context, string) (domain.EngineeringCategory, error) {
	return f.w().cat, f.err
}
func (f *fakeStore) ListCategories(context.Context, string, int) (store.Page[domain.EngineeringCategory], error) {
	return store.Page[domain.EngineeringCategory]{}, f.err
}
func (f *fakeStore) CreateEngineeringVersion(_ context.Context, v domain.EngineeringVersion, _ store.Idem) (domain.EngineeringVersion, error) {
	v.ID = withID(v.ID)
	f.w().version = v
	return v, f.err
}
func (f *fakeStore) GetEngineeringVersion(context.Context, string) (domain.EngineeringVersion, error) {
	return f.w().version, f.err
}
func (f *fakeStore) ListEngineeringVersions(context.Context, string, string, int) (store.Page[domain.EngineeringVersion], error) {
	return store.Page[domain.EngineeringVersion]{}, f.err
}
func (f *fakeStore) LastVersionCode(context.Context, string) (string, error) { return "", f.err }
func (f *fakeStore) PutIdentifier(context.Context, string, string, string) error {
	return f.err
}
func (f *fakeStore) ResolvePart(context.Context, string) (string, bool, error) {
	return "", false, f.err
}
func (f *fakeStore) CreateReleasePolicy(_ context.Context, p domain.ReleasePolicy, _ store.Idem) (domain.ReleasePolicy, error) {
	p.ID = withID(p.ID)
	f.w().policy = p
	return p, f.err
}
func (f *fakeStore) GetReleasePolicy(context.Context, string) (domain.ReleasePolicy, error) {
	return f.w().policy, f.err
}
func (f *fakeStore) ListReleasePolicies(context.Context, string, int) (store.Page[domain.ReleasePolicy], error) {
	return store.Page[domain.ReleasePolicy]{}, f.err
}
func (f *fakeStore) PutSiteMapping(context.Context, domain.SiteMapping) error { return f.err }
func (f *fakeStore) CreateReleaseSession(_ context.Context, in store.ReleaseSessionInput, _ store.Idem) (store.ReleaseSessionRecord, error) {
	rec := store.ReleaseSessionRecord{Session: domain.ReleaseSession{ID: ids.New(), Number: in.Number, CreatedBy: in.CreatedBy, Source: in.Source, PolicyID: in.PolicyID}}
	for _, le := range in.LegalEntities {
		rec.Lines = append(rec.Lines, domain.ReleaseLine{ID: ids.New(), LegalEntityID: le, ProductID: in.ProductID, EngineeringVersion: in.VersionCode, Status: domain.LineSent})
	}
	f.w().session = rec
	return rec, f.err
}
func (f *fakeStore) GetReleaseSession(context.Context, string) (store.ReleaseSessionRecord, error) {
	return f.w().session, f.err
}
func (f *fakeStore) ListReleaseSessions(context.Context, string, int) (store.Page[domain.ReleaseSession], error) {
	return store.Page[domain.ReleaseSession]{}, f.err
}
func (f *fakeStore) ListReleaseLines(context.Context, store.ReleaseLineFilter, string, int) (store.Page[domain.ReleaseLine], error) {
	return store.Page[domain.ReleaseLine]{}, f.err
}
func (f *fakeStore) DecideReleaseLine(_ context.Context, id, actor, reason string, accept bool) (domain.ReleaseLine, error) {
	line := f.w().line
	line.ID = id
	if accept {
		line.Status, line.AcceptedBy = domain.LineAccepted, actor
	} else {
		line.Status, line.AcceptedBy, line.Reason = domain.LineRejected, actor, reason
	}
	return line, f.err
}
func (f *fakeStore) CreateReadinessPolicy(_ context.Context, p domain.ReadinessPolicy, _ store.Idem) (domain.ReadinessPolicy, error) {
	p.ID = withID(p.ID)
	f.w().readiness = p
	return p, f.err
}
func (f *fakeStore) GetReadinessPolicy(context.Context, string) (domain.ReadinessPolicy, error) {
	return f.w().readiness, f.err
}
func (f *fakeStore) ListReadinessPolicies(context.Context, string, int) (store.Page[domain.ReadinessPolicy], error) {
	return store.Page[domain.ReadinessPolicy]{}, f.err
}
func (f *fakeStore) GetReleasedProduct(context.Context, string) (store.ReleasedProduct, error) {
	return f.w().released, f.err
}
func (f *fakeStore) ListReleasedProducts(context.Context, string, string, int) (store.Page[store.ReleasedProduct], error) {
	return store.Page[store.ReleasedProduct]{}, f.err
}
func (f *fakeStore) SignReadinessCheck(context.Context, string, string, string) (store.ReleasedProduct, error) {
	return f.w().released, f.err
}
func (f *fakeStore) EvaluateReleasedProduct(context.Context, string) (store.ReleasedProduct, error) {
	return f.w().released, f.err
}
func (f *fakeStore) CreateDocumentType(_ context.Context, t domain.DocumentType, _ store.Idem) (domain.DocumentType, error) {
	t.ID = withID(t.ID)
	f.w().docType = t
	return t, f.err
}
func (f *fakeStore) GetDocumentType(context.Context, string) (domain.DocumentType, error) {
	return f.w().docType, f.err
}
func (f *fakeStore) ListDocumentTypes(context.Context, string, int) (store.Page[domain.DocumentType], error) {
	return store.Page[domain.DocumentType]{}, f.err
}
func (f *fakeStore) CreateDocument(_ context.Context, d domain.Document, _ store.Idem) (domain.Document, error) {
	d.ID = withID(d.ID)
	if d.Status == "" {
		d.Status = domain.DocInWork
	}
	f.w().doc = d
	return d, f.err
}
func (f *fakeStore) GetDocument(context.Context, string) (domain.Document, error) {
	return f.w().doc, f.err
}
func (f *fakeStore) ListDocuments(context.Context, string, string, int) (store.Page[domain.Document], error) {
	return store.Page[domain.Document]{}, f.err
}
func (f *fakeStore) TransitionDocument(_ context.Context, id, to, _ string, sig *domain.Signature) (domain.Document, error) {
	d := f.w().doc
	d.ID, d.Status = id, to
	if sig != nil {
		d.SignedAt = sig.SignedAt
		f.w().sigs = append(f.w().sigs, *sig)
	}
	f.w().doc = d
	return d, f.err
}
func (f *fakeStore) ListSignatures(context.Context, string) ([]domain.Signature, error) {
	return f.w().sigs, f.err
}
func (f *fakeStore) DocumentWhereUsed(context.Context, string) ([]domain.DocumentWhereUsed, error) {
	return nil, f.err
}
func (f *fakeStore) SetDocumentStructure(context.Context, string, []string) error { return f.err }
func (f *fakeStore) ImportEbom(_ context.Context, in store.EbomImportInput, _ store.Idem) (store.EbomImportRecord, error) {
	rec := store.EbomImportRecord{ID: ids.New(), Source: in.Source, PartNumber: in.Payload.PartNumber, Revision: in.Payload.Revision, Status: domain.EbomDraft, Payload: in.Payload, BOMVersionID: ids.New(), EcrID: ids.New()}
	f.w().ebom = rec
	return rec, f.err
}
func (f *fakeStore) GetEbomImport(context.Context, string) (store.EbomImportRecord, error) {
	return f.w().ebom, f.err
}
func (f *fakeStore) ListEbomImports(context.Context, string, int) (store.Page[store.EbomImportRecord], error) {
	return store.Page[store.EbomImportRecord]{}, f.err
}
