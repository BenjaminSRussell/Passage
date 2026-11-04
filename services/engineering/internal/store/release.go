package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	engevents "github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

const (
	aggregateReleaseSession = "releasesession"
	aggregateReleaseLine    = "releaseline"
	aggregateReleasedProd   = "releasedproduct"
)

func (s *Postgres) CreateReleasePolicy(ctx context.Context, p domain.ReleasePolicy, idem Idem) (domain.ReleasePolicy, error) {
	if p.ID == "" {
		p.ID = ids.New()
	}
	if p.RequireAcceptance == nil {
		p.RequireAcceptance = map[string]bool{}
	}
	return createOnce(s, ctx, idem, p.ID, p, func(ctx context.Context, tx pgx.Tx, tenantID string, v domain.ReleasePolicy) error {
		raw, err := json.Marshal(v.RequireAcceptance)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into release_policy (tenant_id, id, code, copy_bom, activate_bom, copy_route, activate_route, copy_attributes, copy_order_settings, copy_documents, require_acceptance)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			tenantID, v.ID, v.Code, v.CopyBOM, v.ActivateBOM, v.CopyRoute, v.ActivateRoute, v.CopyAttributes, v.CopyOrderSettings, v.CopyDocuments, raw)
		return err
	}, s.GetReleasePolicy)
}

func (s *Postgres) GetReleasePolicy(ctx context.Context, id string) (domain.ReleasePolicy, error) {
	var p domain.ReleasePolicy
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		return scanReleasePolicy(tx.QueryRow(ctx, `select id::text, code, copy_bom, activate_bom, copy_route, activate_route, copy_attributes, copy_order_settings, copy_documents, require_acceptance
			from release_policy where tenant_id = $1 and id = $2::uuid`, tenantID, id), &p)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return p, httpx.NewProblem(http.StatusNotFound, "ENG-RELEASE-POLICY-NOTFOUND", "release policy not found")
	}
	return p, err
}

func scanReleasePolicy(row pgx.Row, p *domain.ReleasePolicy) error {
	var raw []byte
	if err := row.Scan(&p.ID, &p.Code, &p.CopyBOM, &p.ActivateBOM, &p.CopyRoute, &p.ActivateRoute, &p.CopyAttributes, &p.CopyOrderSettings, &p.CopyDocuments, &raw); err != nil {
		return err
	}
	p.RequireAcceptance = map[string]bool{}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, &p.RequireAcceptance)
}

func (s *Postgres) ListReleasePolicies(ctx context.Context, afterID string, limit int) (Page[domain.ReleasePolicy], error) {
	return listSimple(s, ctx, afterID, limit, `select id::text, code, copy_bom, activate_bom, copy_route, activate_route, copy_attributes, copy_order_settings, copy_documents, require_acceptance
		from release_policy where tenant_id = $1 and id > $2::uuid order by id limit $3`,
		func(row pgx.Row) (domain.ReleasePolicy, error) {
			var p domain.ReleasePolicy
			return p, scanReleasePolicy(row, &p)
		})
}

func (s *Postgres) PutSiteMapping(ctx context.Context, m domain.SiteMapping) error {
	return withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		_, err := tx.Exec(ctx, `insert into site_mapping (tenant_id, engineering_le_id, operational_le_id, engineering_site, operational_site)
			values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, engineering_le_id, operational_le_id, engineering_site) do update set operational_site = excluded.operational_site`,
			tenantID, m.EngineeringLEID, m.OperationalLEID, m.EngineeringSite, m.OperationalSite)
		return err
	})
}

func (s *Postgres) CreateReadinessPolicy(ctx context.Context, p domain.ReadinessPolicy, idem Idem) (domain.ReadinessPolicy, error) {
	if p.ID == "" {
		p.ID = ids.New()
	}
	if p.TargetState == "" {
		p.TargetState = domain.ReleasedActive
	}
	return createOnce(s, ctx, idem, p.ID, p, func(ctx context.Context, tx pgx.Tx, tenantID string, v domain.ReadinessPolicy) error {
		raw, err := json.Marshal(v.Checks)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into readiness_policy (tenant_id, id, code, checks, target_state, continuous) values ($1, $2, $3, $4, $5, $6)`,
			tenantID, v.ID, v.Code, raw, v.TargetState, v.Continuous)
		return err
	}, s.GetReadinessPolicy)
}

func (s *Postgres) GetReadinessPolicy(ctx context.Context, id string) (domain.ReadinessPolicy, error) {
	var p domain.ReadinessPolicy
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		return scanReadinessPolicy(tx.QueryRow(ctx, `select id::text, code, checks, target_state, continuous from readiness_policy where tenant_id = $1 and id = $2::uuid`, tenantID, id), &p)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return p, httpx.NewProblem(http.StatusNotFound, "ENG-READINESS-POLICY-NOTFOUND", "readiness policy not found")
	}
	return p, err
}

func scanReadinessPolicy(row pgx.Row, p *domain.ReadinessPolicy) error {
	var raw []byte
	if err := row.Scan(&p.ID, &p.Code, &raw, &p.TargetState, &p.Continuous); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, &p.Checks)
}

func (s *Postgres) ListReadinessPolicies(ctx context.Context, afterID string, limit int) (Page[domain.ReadinessPolicy], error) {
	return listSimple(s, ctx, afterID, limit, `select id::text, code, checks, target_state, continuous from readiness_policy where tenant_id = $1 and id > $2::uuid order by id limit $3`,
		func(row pgx.Row) (domain.ReadinessPolicy, error) {
			var p domain.ReadinessPolicy
			return p, scanReadinessPolicy(row, &p)
		})
}

func ActsForLegalEntity(p tenant.Principal, legalEntity string) bool {
	if p.LegalEntityID == "" {
		return tenant.IsTenantAdmin(p)
	}
	for _, part := range strings.Split(p.LegalEntityID, ",") {
		if strings.TrimSpace(part) == legalEntity {
			return true
		}
	}
	return false
}

func outOfScope(legalEntity string) error {
	return &domain.Error{Code: domain.CodeReleaseScope, Detail: "the caller is not scoped to legal entity " + legalEntity}
}

func (s *Postgres) CreateReleaseSession(ctx context.Context, in ReleaseSessionInput, idem Idem) (ReleaseSessionRecord, error) {
	if in.Source == "" {
		in.Source = domain.ReleaseManual
	}
	var rec ReleaseSessionRecord
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if found {
			loaded, err := getReleaseSession(ctx, tx, p.TenantID, id)
			rec = loaded
			return err
		}
		if in.EngineeringLEID != "" && !ActsForLegalEntity(p, in.EngineeringLEID) {
			return outOfScope(in.EngineeringLEID)
		}
		if in.Number == "" {
			var seq int
			if err := tx.QueryRow(ctx, `select nextval('release_number_seq')`).Scan(&seq); err != nil {
				return err
			}
			in.Number = domain.ReleaseNumber(time.Now().UTC().Year(), seq)
		}
		session := domain.ReleaseSession{ID: ids.New(), Number: in.Number, CreatedBy: in.CreatedBy, Source: in.Source, PolicyID: in.PolicyID, EcoID: in.EcoID, EngineeringLEID: in.EngineeringLEID, ReadinessPolicyID: in.ReadinessPolicyID}
		policy, err := getReleasePolicyTx(ctx, tx, p.TenantID, in.PolicyID)
		if err != nil {
			return err
		}
		lines := domain.ApplyRelease(session, policy, in.ProductID, in.VersionCode, in.LegalEntities)
		for _, l := range lines {
			if l.Status == domain.LineAccepted && !ActsForLegalEntity(p, l.LegalEntityID) {
				return outOfScope(l.LegalEntityID)
			}
		}
		if _, err := tx.Exec(ctx, `insert into release_session (tenant_id, id, number, created_by, source, policy_id, eco_id, engineering_le_id, readiness_policy_id)
			values ($1, $2, $3, $4, $5, $6, nullif($7, '')::uuid, $8, nullif($9, '')::uuid)`,
			p.TenantID, session.ID, session.Number, session.CreatedBy, session.Source, session.PolicyID, session.EcoID, session.EngineeringLEID, session.ReadinessPolicyID); err != nil {
			return err
		}
		for i := range lines {
			if lines[i].ID == "" {
				lines[i].ID = ids.New()
			}
			if err := applyAcceptedLine(ctx, tx, p, policy, in, &lines[i]); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into release_line (tenant_id, id, session_id, product_id, engineering_version, legal_entity_id, status, reason, accepted_by, retries, copied_bom_version_id, copied_route_version_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, nullif($11, '')::uuid, nullif($12, '')::uuid)`,
				p.TenantID, lines[i].ID, session.ID, lines[i].ProductID, lines[i].EngineeringVersion, lines[i].LegalEntityID, lines[i].Status, lines[i].Reason, lines[i].AcceptedBy, lines[i].Retries, lines[i].CopiedBOMVersionID, lines[i].CopiedRouteVersionID); err != nil {
				return err
			}
			if lines[i].Status == domain.LineAccepted {
				if err := emit(ctx, tx, p, session.ID, engevents.ReleaseLineAccepted, aggregateReleaseLine, lines[i].ID, lines[i]); err != nil {
					return err
				}
			}
		}
		if err := emit(ctx, tx, p, session.ID, engevents.ReleaseSessionCreated, aggregateReleaseSession, session.ID, session); err != nil {
			return err
		}
		if err := remember(ctx, tx, p.TenantID, idem, session.ID); err != nil {
			return err
		}
		rec = ReleaseSessionRecord{Session: session, Lines: lines}
		return nil
	})
	return rec, conflictOnDuplicate(err)
}

func getReleasePolicyTx(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.ReleasePolicy, error) {
	var p domain.ReleasePolicy
	err := scanReleasePolicy(tx.QueryRow(ctx, `select id::text, code, copy_bom, activate_bom, copy_route, activate_route, copy_attributes, copy_order_settings, copy_documents, require_acceptance
		from release_policy where tenant_id = $1 and id = $2::uuid`, tenantID, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, httpx.NewProblem(http.StatusNotFound, "ENG-RELEASE-POLICY-NOTFOUND", "release policy not found")
	}
	return p, err
}

func applyAcceptedLine(ctx context.Context, tx pgx.Tx, p tenant.Principal, policy domain.ReleasePolicy, in ReleaseSessionInput, line *domain.ReleaseLine) error {
	if line.Status != domain.LineAccepted {
		return nil
	}
	engineeringSite, site, err := mappedSites(ctx, tx, p.TenantID, in.EngineeringLEID, line.LegalEntityID)
	if err != nil {
		return err
	}
	if policy.CopyBOM {
		copied, err := copyActiveBOM(ctx, tx, p, line.ProductID, engineeringSite, site, policy.ActivateBOM)
		if err != nil {
			return err
		}
		line.CopiedBOMVersionID = copied
	}
	if policy.CopyRoute {
		copied, err := copyActiveRoute(ctx, tx, p, line.ProductID, engineeringSite, site, policy.ActivateRoute)
		if err != nil {
			return err
		}
		line.CopiedRouteVersionID = copied
	}
	facts := in.Facts
	if err := tx.QueryRow(ctx, `select
			exists(select 1 from bom_version where tenant_id = $1 and product_id = $2 and site_id = $3 and status = 'active'),
			exists(select 1 from route_version where tenant_id = $1 and product_id = $2 and site_id = $3 and status = 'active')`,
		p.TenantID, line.ProductID, site).Scan(&facts.ActiveBOM, &facts.ActiveRoute); err != nil {
		return err
	}
	return upsertReleasedProduct(ctx, tx, p, in, facts, *line)
}

func mappedSites(ctx context.Context, tx pgx.Tx, tenantID, engineeringLE, operationalLE string) (engineeringSite, operationalSite string, err error) {
	err = tx.QueryRow(ctx, `select engineering_site, operational_site from site_mapping where tenant_id = $1 and engineering_le_id = $2 and operational_le_id = $3 order by engineering_site limit 1`,
		tenantID, engineeringLE, operationalLE).Scan(&engineeringSite, &operationalSite)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", operationalLE, nil
	}
	if operationalSite == "" {
		operationalSite = operationalLE
	}
	return engineeringSite, operationalSite, err
}

func copySource(engineeringSite, site string) (condition, siteArg string) {
	if engineeringSite != "" {
		return `product_id = $2 and status = 'active' and site_id = $3`, engineeringSite
	}
	return `product_id = $2 and status = 'active' and copied_from_version_id is null and site_id <> $3`, site
}

func copyActiveBOM(ctx context.Context, tx pgx.Tx, p tenant.Principal, productID, engineeringSite, site string, activate bool) (string, error) {
	if err := lockProducts(ctx, tx, p.TenantID, productID); err != nil {
		return "", err
	}
	cond, siteArg := copySource(engineeringSite, site)
	found, err := loadVersions(ctx, tx, p.TenantID, cond, productID, siteArg)
	if err != nil || len(found) == 0 {
		return "", err
	}
	src := found[0]
	for _, v := range found[1:] {
		if v.EffectiveFrom.After(src.EffectiveFrom) {
			src = v
		}
	}
	copy := src
	copy.ID, copy.SiteID, copy.CopiedFrom, copy.Status = ids.New(), site, src.ID, domain.StatusDraft
	if activate {
		copy.Status = domain.StatusActive
		if _, err := tx.Exec(ctx, `update bom_version set status = 'expired', effective_to = greatest(effective_from, $5::date), version = version + 1
			where tenant_id = $1 and product_id = $2 and site_id = $3 and usage = $4 and category = $6 and status = 'active' and copied_from_version_id is not null`,
			p.TenantID, productID, site, usageOrDefault(copy.Usage), copy.EffectiveFrom.AddDate(0, 0, -1), categoryOrDefault(copy.Category)); err != nil {
			return "", err
		}
	}
	if err := insertVersion(ctx, tx, p.TenantID, copy); err != nil {
		return "", err
	}
	if activate {
		if err := ensureNoActiveOverlap(ctx, tx, p.TenantID, copy); err != nil {
			return "", err
		}
		if err := emit(ctx, tx, p, copy.ID, engevents.BomVersionActivated, aggregateBomVersion, copy.ID, copy.ActivatedEvent(src.ID)); err != nil {
			return "", err
		}
	}
	return copy.ID, nil
}

func copyActiveRoute(ctx context.Context, tx pgx.Tx, p tenant.Principal, productID, engineeringSite, site string, activate bool) (string, error) {
	cond, siteArg := copySource(engineeringSite, site)
	var srcID string
	err := tx.QueryRow(ctx, `select id::text from route_version where tenant_id = $1 and (`+cond+`) order by effective_from desc, id limit 1`, p.TenantID, productID, siteArg).Scan(&srcID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	src, err := getRouteVersion(ctx, tx, p.TenantID, srcID)
	if err != nil {
		return "", err
	}
	copy := src
	copy.ID, copy.SiteID, copy.CopiedFrom, copy.VersionNo, copy.Status = ids.New(), site, src.ID, 0, domain.StatusDraft
	if err := insertRouteVersion(ctx, tx, p.TenantID, copy); err != nil {
		return "", err
	}
	if !activate {
		return copy.ID, nil
	}
	if _, err := tx.Exec(ctx, `update route_version set status = 'expired', effective_to = greatest(effective_from, $4::date), version = version + 1
		where tenant_id = $1 and product_id = $2 and site_id = $3 and status = 'active' and copied_from_version_id is not null and id <> $5::uuid`,
		p.TenantID, productID, site, copy.EffectiveFrom.AddDate(0, 0, -1), copy.ID); err != nil {
		return "", err
	}
	activated, err := activateRouteInTx(ctx, tx, p, copy.ID)
	if err != nil {
		return "", err
	}
	return activated.ID, nil
}

func activateRouteInTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) (domain.RouteVersion, error) {
	v, err := getRouteVersion(ctx, tx, p.TenantID, id)
	if err != nil {
		return v, err
	}
	active, err := activeRouteVersions(ctx, tx, p.TenantID, v)
	if err != nil {
		return v, err
	}
	if err := domain.CheckRouteNoOverlap(active, v); err != nil {
		return v, err
	}
	resources, err := loadSiteResources(ctx, tx, p.TenantID, v.SiteID)
	if err != nil {
		return v, err
	}
	if err := domain.CheckRequirements(v, resources); err != nil {
		return v, err
	}
	if _, err := tx.Exec(ctx, `update route_version set status = 'active', version = version + 1 where tenant_id = $1 and id = $2::uuid`, p.TenantID, id); err != nil {
		return v, err
	}
	v.Status, v.Version = domain.StatusActive, v.Version+1
	return v, emit(ctx, tx, p, id, engevents.RouteVersionActivated, aggregateRouteVersion, id, v.ActivatedEvent())
}

func upsertReleasedProduct(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ReleaseSessionInput, facts domain.ReadinessFacts, line domain.ReleaseLine) error {
	id := ids.New()
	var existing string
	err := tx.QueryRow(ctx, `select id::text from released_product where tenant_id = $1 and product_id = $2 and legal_entity_id = $3`, p.TenantID, line.ProductID, line.LegalEntityID).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if existing != "" {
		id = existing
		if _, err := tx.Exec(ctx, `update released_product set engineering_version = $3, session_id = $4, state = $5 where tenant_id = $1 and id = $2::uuid`,
			p.TenantID, id, line.EngineeringVersion, line.SessionID, domain.ReadinessPending); err != nil {
			return err
		}
	} else if _, err := tx.Exec(ctx, `insert into released_product (tenant_id, id, product_id, legal_entity_id, engineering_version, session_id, state)
		values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, id, line.ProductID, line.LegalEntityID, line.EngineeringVersion, line.SessionID, domain.ReadinessPending); err != nil {
		return err
	}
	policy, err := readinessPolicyFor(ctx, tx, p.TenantID, in)
	if err != nil {
		return err
	}
	results := domain.EvaluateReadiness(policy, facts, nil)
	state, _ := domain.ReadinessState(policy, results)
	if _, err := tx.Exec(ctx, `update released_product set state = $3 where tenant_id = $1 and id = $2::uuid`, p.TenantID, id, state); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from readiness_check_result where tenant_id = $1 and released_product_id = $2::uuid`, p.TenantID, id); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := tx.Exec(ctx, `insert into readiness_check_result (tenant_id, released_product_id, check_key, kind, status, signed_by) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, id, r.Key, r.Kind, r.Status, r.SignedBy); err != nil {
			return err
		}
	}
	return emit(ctx, tx, p, line.SessionID, engevents.ReleasedProductChanged, aggregateReleasedProd, id, ReleasedProduct{ID: id, ProductID: line.ProductID, LegalEntityID: line.LegalEntityID, EngineeringVersion: line.EngineeringVersion, SessionID: line.SessionID, State: state})
}

func readinessPolicyFor(ctx context.Context, tx pgx.Tx, tenantID string, in ReleaseSessionInput) (domain.ReadinessPolicy, error) {
	id := in.ReadinessPolicyID
	if id == "" {
		_ = tx.QueryRow(ctx, `select coalesce(c.readiness_policy_id::text, '') from engineering_version v
			join engineering_category c on c.tenant_id = v.tenant_id and c.id = v.category_id
			where v.tenant_id = $1 and v.product_id = $2 order by v.id desc limit 1`, tenantID, in.ProductID).Scan(&id)
	}
	if id == "" {
		return domain.ReadinessPolicy{TargetState: domain.ReleasedActive}, nil
	}
	var p domain.ReadinessPolicy
	err := scanReadinessPolicy(tx.QueryRow(ctx, `select id::text, code, checks, target_state, continuous from readiness_policy where tenant_id = $1 and id = $2::uuid`, tenantID, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ReadinessPolicy{TargetState: domain.ReleasedActive}, nil
	}
	return p, err
}

func (s *Postgres) GetReleaseSession(ctx context.Context, id string) (ReleaseSessionRecord, error) {
	var rec ReleaseSessionRecord
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) (err error) {
		rec, err = getReleaseSession(ctx, tx, tenantID, id)
		return err
	})
	return rec, err
}

func getReleaseSession(ctx context.Context, tx pgx.Tx, tenantID, id string) (ReleaseSessionRecord, error) {
	var rec ReleaseSessionRecord
	err := tx.QueryRow(ctx, `select id::text, number, created_by, source, policy_id::text, coalesce(eco_id::text, ''), engineering_le_id, coalesce(readiness_policy_id::text, '') from release_session where tenant_id = $1 and id = $2::uuid`, tenantID, id).
		Scan(&rec.Session.ID, &rec.Session.Number, &rec.Session.CreatedBy, &rec.Session.Source, &rec.Session.PolicyID, &rec.Session.EcoID, &rec.Session.EngineeringLEID, &rec.Session.ReadinessPolicyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, httpx.NewProblem(http.StatusNotFound, "ENG-RELEASE-NOTFOUND", "release session not found")
	}
	if err != nil {
		return rec, err
	}
	rows, err := tx.Query(ctx, `select id::text, session_id::text, product_id, engineering_version, legal_entity_id, status, reason, accepted_by, retries, coalesce(copied_bom_version_id::text, ''), coalesce(copied_route_version_id::text, '')
		from release_line where tenant_id = $1 and session_id = $2::uuid order by id`, tenantID, id)
	if err != nil {
		return rec, err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.ReleaseLine
		if err := rows.Scan(&l.ID, &l.SessionID, &l.ProductID, &l.EngineeringVersion, &l.LegalEntityID, &l.Status, &l.Reason, &l.AcceptedBy, &l.Retries, &l.CopiedBOMVersionID, &l.CopiedRouteVersionID); err != nil {
			return rec, err
		}
		rec.Lines = append(rec.Lines, l)
	}
	return rec, rows.Err()
}

func (s *Postgres) ListReleaseSessions(ctx context.Context, afterID string, limit int) (Page[domain.ReleaseSession], error) {
	return listSimple(s, ctx, afterID, limit, `select id::text, number, created_by, source, policy_id::text, coalesce(eco_id::text, ''), engineering_le_id, coalesce(readiness_policy_id::text, '') from release_session where tenant_id = $1 and id > $2::uuid order by id limit $3`,
		func(row pgx.Row) (domain.ReleaseSession, error) {
			var s domain.ReleaseSession
			return s, row.Scan(&s.ID, &s.Number, &s.CreatedBy, &s.Source, &s.PolicyID, &s.EcoID, &s.EngineeringLEID, &s.ReadinessPolicyID)
		})
}

func (s *Postgres) ListReleaseLines(ctx context.Context, f ReleaseLineFilter, afterID string, limit int) (Page[domain.ReleaseLine], error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	var items []domain.ReleaseLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, session_id::text, product_id, engineering_version, legal_entity_id, status, reason, accepted_by, retries, coalesce(copied_bom_version_id::text, ''), coalesce(copied_route_version_id::text, '')
			from release_line where tenant_id = $1 and id > $2::uuid and ($3 = '' or status = $3) and ($4 = '' or legal_entity_id = $4) order by id limit $5`,
			p.TenantID, afterID, f.Status, f.LegalEntityID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l domain.ReleaseLine
			if err := rows.Scan(&l.ID, &l.SessionID, &l.ProductID, &l.EngineeringVersion, &l.LegalEntityID, &l.Status, &l.Reason, &l.AcceptedBy, &l.Retries, &l.CopiedBOMVersionID, &l.CopiedRouteVersionID); err != nil {
				return err
			}
			items = append(items, l)
		}
		return rows.Err()
	})
	return trimPage(items, limit, func(l domain.ReleaseLine) string { return l.ID }), err
}

func (s *Postgres) DecideReleaseLine(ctx context.Context, id, actor, reason string, accept bool) (domain.ReleaseLine, error) {
	var line domain.ReleaseLine
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select id::text, session_id::text, product_id, engineering_version, legal_entity_id, status, reason, accepted_by, retries, coalesce(copied_bom_version_id::text, ''), coalesce(copied_route_version_id::text, '')
			from release_line where tenant_id = $1 and id = $2::uuid for update`, p.TenantID, id).
			Scan(&line.ID, &line.SessionID, &line.ProductID, &line.EngineeringVersion, &line.LegalEntityID, &line.Status, &line.Reason, &line.AcceptedBy, &line.Retries, &line.CopiedBOMVersionID, &line.CopiedRouteVersionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "ENG-RELEASE-LINE-NOTFOUND", "release line not found")
		}
		if err != nil {
			return err
		}
		if !ActsForLegalEntity(p, line.LegalEntityID) {
			return outOfScope(line.LegalEntityID)
		}
		if accept {
			err = line.Accept(actor, reason)
		} else {
			err = line.Reject(actor, reason)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update release_line set status = $3, reason = $4, accepted_by = $5 where tenant_id = $1 and id = $2::uuid`,
			p.TenantID, line.ID, line.Status, line.Reason, line.AcceptedBy); err != nil {
			return err
		}
		if !accept {
			return nil
		}
		session, err := getReleaseSession(ctx, tx, p.TenantID, line.SessionID)
		if err != nil {
			return err
		}
		policy, err := getReleasePolicyTx(ctx, tx, p.TenantID, session.Session.PolicyID)
		if err != nil {
			return err
		}
		in := ReleaseSessionInput{
			CreatedBy: session.Session.CreatedBy, PolicyID: session.Session.PolicyID, EcoID: session.Session.EcoID, ProductID: line.ProductID, VersionCode: line.EngineeringVersion,
			EngineeringLEID: session.Session.EngineeringLEID, ReadinessPolicyID: session.Session.ReadinessPolicyID,
		}
		if err := applyAcceptedLine(ctx, tx, p, policy, in, &line); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update release_line set copied_bom_version_id = nullif($3, '')::uuid, copied_route_version_id = nullif($4, '')::uuid where tenant_id = $1 and id = $2::uuid`,
			p.TenantID, line.ID, line.CopiedBOMVersionID, line.CopiedRouteVersionID); err != nil {
			return err
		}
		return emit(ctx, tx, p, line.SessionID, engevents.ReleaseLineAccepted, aggregateReleaseLine, line.ID, line)
	})
	return line, err
}

func (s *Postgres) GetReleasedProduct(ctx context.Context, id string) (ReleasedProduct, error) {
	var rec ReleasedProduct
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) (err error) {
		rec, err = getReleasedProduct(ctx, tx, tenantID, id)
		return err
	})
	return rec, err
}

func getReleasedProduct(ctx context.Context, tx pgx.Tx, tenantID, id string) (ReleasedProduct, error) {
	var rec ReleasedProduct
	err := tx.QueryRow(ctx, `select r.id::text, r.product_id, r.legal_entity_id, r.engineering_version, coalesce(r.session_id::text, ''), r.state, coalesce(s.created_by, ''), coalesce(s.readiness_policy_id::text, '')
		from released_product r left join release_session s on s.tenant_id = r.tenant_id and s.id = r.session_id
		where r.tenant_id = $1 and r.id = $2::uuid`, tenantID, id).
		Scan(&rec.ID, &rec.ProductID, &rec.LegalEntityID, &rec.EngineeringVersion, &rec.SessionID, &rec.State, &rec.SessionCreator, &rec.ReadinessPolicyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, httpx.NewProblem(http.StatusNotFound, "ENG-RELEASED-NOTFOUND", "released product not found")
	}
	if err != nil {
		return rec, err
	}
	rows, err := tx.Query(ctx, `select check_key, kind, status, signed_by from readiness_check_result where tenant_id = $1 and released_product_id = $2::uuid order by check_key`, tenantID, id)
	if err != nil {
		return rec, err
	}
	defer rows.Close()
	for rows.Next() {
		var c domain.ReadinessCheckResult
		if err := rows.Scan(&c.Key, &c.Kind, &c.Status, &c.SignedBy); err != nil {
			return rec, err
		}
		rec.Checks = append(rec.Checks, c)
	}
	return rec, rows.Err()
}

func (s *Postgres) ListReleasedProducts(ctx context.Context, legalEntityID, afterID string, limit int) (Page[ReleasedProduct], error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	var items []ReleasedProduct
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id::text, r.product_id, r.legal_entity_id, r.engineering_version, coalesce(r.session_id::text, ''), r.state, coalesce(s.created_by, '')
			from released_product r left join release_session s on s.tenant_id = r.tenant_id and s.id = r.session_id
			where r.tenant_id = $1 and r.id > $2::uuid and ($3 = '' or r.legal_entity_id = $3) order by r.id limit $4`, p.TenantID, afterID, legalEntityID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rec ReleasedProduct
			if err := rows.Scan(&rec.ID, &rec.ProductID, &rec.LegalEntityID, &rec.EngineeringVersion, &rec.SessionID, &rec.State, &rec.SessionCreator); err != nil {
				return err
			}
			items = append(items, rec)
		}
		return rows.Err()
	})
	return trimPage(items, limit, func(r ReleasedProduct) string { return r.ID }), err
}

func (s *Postgres) SignReadinessCheck(ctx context.Context, releasedProductID, key, signer string) (ReleasedProduct, error) {
	var rec ReleasedProduct
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		loaded, err := getReleasedProduct(ctx, tx, p.TenantID, releasedProductID)
		if err != nil {
			return err
		}
		signed, err := domain.SignManualCheck(loaded.Checks, key, signer, loaded.SessionCreator)
		if err != nil {
			return err
		}
		for _, c := range signed {
			if _, err := tx.Exec(ctx, `update readiness_check_result set status = $4, signed_by = $5, evaluated_at = now() where tenant_id = $1 and released_product_id = $2::uuid and check_key = $3`,
				p.TenantID, releasedProductID, c.Key, c.Status, c.SignedBy); err != nil {
				return err
			}
		}
		return refreshReadiness(ctx, tx, p, releasedProductID, signed)
	})
	if err != nil {
		return rec, err
	}
	return s.GetReleasedProduct(ctx, releasedProductID)
}

func refreshReadiness(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, results []domain.ReadinessCheckResult) error {
	rec, err := getReleasedProduct(ctx, tx, p.TenantID, id)
	if err != nil {
		return err
	}
	policy, err := readinessPolicyFor(ctx, tx, p.TenantID, ReleaseSessionInput{ProductID: rec.ProductID, ReadinessPolicyID: rec.ReadinessPolicyID})
	if err != nil {
		return err
	}
	state, _ := domain.ReadinessState(policy, results)
	if _, err := tx.Exec(ctx, `update released_product set state = $3 where tenant_id = $1 and id = $2::uuid`, p.TenantID, id, state); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `delete from readiness_check_result where tenant_id = $1 and released_product_id = $2::uuid`, p.TenantID, id); err != nil {
		return err
	}
	for _, r := range results {
		if _, err := tx.Exec(ctx, `insert into readiness_check_result (tenant_id, released_product_id, check_key, kind, status, signed_by) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, id, r.Key, r.Kind, r.Status, r.SignedBy); err != nil {
			return err
		}
	}
	return nil
}
