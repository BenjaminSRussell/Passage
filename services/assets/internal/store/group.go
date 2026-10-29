package store

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
)

// Codes raised by group assets.
const (
	CodeGroupNotFound = "AST-0032"
	CodeGroupCodeDup  = "AST-0033"
	// MaxGroupMembers bounds the members assigned in one request.
	MaxGroupMembers = 1000
)

// GroupAssetInput is a validated request to pool assets into a group asset on
// one tax book (FR-37.16). Each member asset contributes its book with
// BookCode. The profile (method, factor, life, convention) is the group's.
type GroupAssetInput struct {
	LegalEntityID        string          `json:"legalEntityId"`
	GroupCode            string          `json:"groupCode"`
	Name                 string          `json:"name"`
	ClassCode            string          `json:"classCode"`
	BookCode             string          `json:"bookCode"`
	TaxYear              int             `json:"taxYear"`
	Method               string          `json:"method"`
	Factor               decimal.Decimal `json:"factor"`
	SwitchToStraightLine bool            `json:"switchToStraightLine"`
	ServiceLifePeriods   int             `json:"serviceLifePeriods"`
	Convention           string          `json:"convention"`
	MemberAssetIDs       []string        `json:"memberAssetIds"`
}

// GroupAsset is a pool of member books with its aggregate basis (the sum of
// the members' cost on the group's book, read live) and the yearly schedule
// on that basis. The group's own depreciation run line is a later slice;
// until then a run gives member books an AST-0035 exception and no charge.
type GroupAsset struct {
	ID                   string          `json:"id"`
	LegalEntityID        string          `json:"legalEntityId"`
	GroupCode            string          `json:"groupCode"`
	Name                 string          `json:"name"`
	ClassCode            string          `json:"classCode"`
	BookCode             string          `json:"bookCode"`
	TaxYear              int             `json:"taxYear"`
	Currency             string          `json:"currency"`
	Method               string          `json:"method"`
	Factor               decimal.Decimal `json:"factor"`
	SwitchToStraightLine bool            `json:"switchToStraightLine"`
	ServiceLifePeriods   int             `json:"serviceLifePeriods"`
	Convention           string          `json:"convention"`
	InServiceDate        Date            `json:"inServiceDate"`
	Status               string          `json:"status"`
	Basis                decimal.Decimal `json:"basis"`
	MemberCount          int             `json:"memberCount"`
	Version              int             `json:"version"`
	CreatedAt            time.Time       `json:"createdAt"`
	Members              []GroupMember   `json:"members,omitempty"`
	Schedule             []GroupYear     `json:"schedule,omitempty"`
}

// Profile is the group's depreciation input on its aggregate basis, starting
// 1 January of the tax year.
func (g GroupAsset) Profile() domain.Input {
	return domain.Input{Cost: g.Basis, Life: g.ServiceLifePeriods, Method: domain.Method(g.Method), Factor: g.Factor,
		SwitchToStraightLine: g.SwitchToStraightLine, InService: domain.GroupStart(g.TaxYear), Convention: domain.Convention(g.Convention), Currency: g.Currency}
}

// GroupMember is one member book of a group asset.
type GroupMember struct {
	AssetID       string          `json:"assetId"`
	AssetNumber   string          `json:"assetNumber"`
	Name          string          `json:"name"`
	AssetBookID   string          `json:"assetBookId"`
	BookStatus    string          `json:"bookStatus"`
	InServiceDate Date            `json:"inServiceDate"`
	Cost          decimal.Decimal `json:"cost"`
}

// GroupYear is one year of the group's projected schedule.
type GroupYear struct {
	Year        string          `json:"year"`
	Amount      decimal.Decimal `json:"amount"`
	Accumulated decimal.Decimal `json:"accumulated"`
	NBV         decimal.Decimal `json:"nbv"`
}

// GroupFilter narrows ListGroupAssets. After is the id of the last group of
// the previous page; Limit is 1..200.
type GroupFilter struct {
	LegalEntityID string
	After         string
	Limit         int
}

// GroupPage is one page of group assets ordered by code, without members.
type GroupPage struct {
	Items      []GroupAsset `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

const groupColumns = `g.id, g.legal_entity_id, g.group_code, g.name, g.class_code, g.book_code, g.tax_year, g.currency, g.method, g.factor,
	g.switch_to_straight_line, g.service_life_periods, g.convention, g.status, g.version, g.created_at,
	coalesce((select sum(b.cost) from group_asset_member m join asset_book b on b.tenant_id = m.tenant_id and b.id = m.asset_book_id
		where m.tenant_id = g.tenant_id and m.group_id = g.id), 0),
	(select count(*) from group_asset_member m where m.tenant_id = g.tenant_id and m.group_id = g.id)`

func scanGroup(row pgx.Row) (GroupAsset, error) {
	var g GroupAsset
	err := row.Scan(&g.ID, &g.LegalEntityID, &g.GroupCode, &g.Name, &g.ClassCode, &g.BookCode, &g.TaxYear, &g.Currency, &g.Method, &g.Factor,
		&g.SwitchToStraightLine, &g.ServiceLifePeriods, &g.Convention, &g.Status, &g.Version, &g.CreatedAt, &g.Basis, &g.MemberCount)
	g.InServiceDate = NewDate(domain.GroupStart(g.TaxYear))
	return g, err
}

// CreateGroupAsset pools the member assets' books with the group's book code
// into a new group asset (FR-37.16). Every member must be an Open book of the
// group's legal entity, class and tax year in one currency (AST-0031), and a
// book belongs to at most one group asset (BR-37.11, AST-0535).
func (s *Postgres) CreateGroupAsset(ctx context.Context, in GroupAssetInput, idempotencyKey string) (GroupAsset, error) {
	p := principal(ctx)
	var out GroupAsset
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "group-asset.create", in, func() (GroupAsset, error) {
			return createGroup(ctx, tx, p.TenantID, in)
		})
		return err
	})
	switch uniqueConstraint(err) {
	case "":
		return out, err
	case "group_asset_member_book_unique":
		return GroupAsset{}, httpx.NewProblem(http.StatusConflict, domain.CodeGroupDuplicate, "a member book was added to another group asset at the same time")
	default:
		return GroupAsset{}, httpx.NewProblem(http.StatusConflict, CodeGroupCodeDup, "a group asset with this code already exists in the legal entity")
	}
}

func createGroup(ctx context.Context, tx pgx.Tx, tenantID string, in GroupAssetInput) (GroupAsset, error) {
	// Lock the member assets in id order (then their books, as every command
	// does) so two overlapping requests cannot deadlock.
	assetIDs := append([]string(nil), in.MemberAssetIDs...)
	sort.Strings(assetIDs)
	type candidate struct {
		assetID, bookID string
	}
	members := make([]domain.GroupMember, 0, len(assetIDs))
	candidates := make([]candidate, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		var m domain.GroupMember
		err := tx.QueryRow(ctx, `select asset_number, legal_entity_id, class_code from asset where tenant_id = $1 and id = $2 for update`,
			tenantID, assetID).Scan(&m.AssetNumber, &m.LegalEntityID, &m.ClassCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return GroupAsset{}, notFound(CodeAssetNotFound, "no asset with id "+assetID)
		}
		if err != nil {
			return GroupAsset{}, err
		}
		var bookID string
		var inService Date
		err = tx.QueryRow(ctx, `select id, book_code, currency, status, in_service_date, cost from asset_book
			where tenant_id = $1 and asset_id = $2 and book_code = $3 for update`, tenantID, assetID, in.BookCode).
			Scan(&bookID, &m.BookCode, &m.Currency, &m.Status, &inService, &m.Cost)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return GroupAsset{}, err
		}
		m.InService = inService.Time
		if bookID != "" {
			var group string
			err := tx.QueryRow(ctx, `select g.group_code from group_asset_member m join group_asset g on g.tenant_id = m.tenant_id and g.id = m.group_id
				where m.tenant_id = $1 and m.asset_book_id = $2`, tenantID, bookID).Scan(&group)
			if err == nil {
				return GroupAsset{}, httpx.NewProblem(http.StatusConflict, domain.CodeGroupDuplicate,
					"asset "+m.AssetNumber+" already belongs to group "+group+" for book "+in.BookCode)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return GroupAsset{}, err
			}
		}
		members = append(members, m)
		candidates = append(candidates, candidate{assetID: assetID, bookID: bookID})
	}
	basis, currency, err := domain.GroupBasis(domain.GroupSpec{LegalEntityID: in.LegalEntityID, ClassCode: in.ClassCode, BookCode: in.BookCode,
		TaxYear: in.TaxYear}, members)
	if err != nil {
		return GroupAsset{}, err
	}
	g := GroupAsset{Basis: basis, Currency: currency, Method: in.Method, Factor: in.Factor, SwitchToStraightLine: in.SwitchToStraightLine,
		ServiceLifePeriods: in.ServiceLifePeriods, Convention: in.Convention, TaxYear: in.TaxYear}
	if _, err := domain.Schedule(g.Profile()); err != nil {
		return GroupAsset{}, err
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into group_asset (tenant_id, id, legal_entity_id, group_code, name, class_code, book_code, tax_year, currency, method,
		factor, switch_to_straight_line, service_life_periods, convention) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		tenantID, id, in.LegalEntityID, in.GroupCode, in.Name, in.ClassCode, in.BookCode, in.TaxYear, currency, in.Method, in.Factor,
		in.SwitchToStraightLine, in.ServiceLifePeriods, in.Convention); err != nil {
		return GroupAsset{}, err
	}
	for _, c := range candidates {
		if _, err := tx.Exec(ctx, `insert into group_asset_member (tenant_id, group_id, asset_book_id, asset_id, legal_entity_id) values ($1, $2, $3, $4, $5)`,
			tenantID, id, c.bookID, c.assetID, in.LegalEntityID); err != nil {
			return GroupAsset{}, err
		}
	}
	return loadGroup(ctx, tx, tenantID, id)
}

// GetGroupAsset reads a group asset with its members and yearly schedule.
func (s *Postgres) GetGroupAsset(ctx context.Context, id string) (GroupAsset, error) {
	p := principal(ctx)
	var out GroupAsset
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadGroup(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListGroupAssets returns one page of group assets ordered by code.
func (s *Postgres) ListGroupAssets(ctx context.Context, f GroupFilter) (GroupPage, error) {
	p := principal(ctx)
	page := GroupPage{Items: []GroupAsset{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+groupColumns+` from group_asset g
			where g.tenant_id = $1
			  and ($2::text = '' or g.legal_entity_id::text = $2)
			  and ($3::text = '' or (g.group_code, g.id) > (select c.group_code, c.id from group_asset c where c.tenant_id = $1 and c.id::text = $3))
			order by g.group_code, g.id limit $4`, p.TenantID, f.LegalEntityID, f.After, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGroup(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, g)
		}
		return rows.Err()
	})
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, err
}

func loadGroup(ctx context.Context, tx pgx.Tx, tenantID, id string) (GroupAsset, error) {
	g, err := scanGroup(tx.QueryRow(ctx, `select `+groupColumns+` from group_asset g where g.tenant_id = $1 and g.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return GroupAsset{}, notFound(CodeGroupNotFound, "no group asset with id "+id)
	}
	if err != nil {
		return GroupAsset{}, err
	}
	rows, err := tx.Query(ctx, `select a.id, a.asset_number, a.name, b.id, b.status, b.in_service_date, b.cost
		from group_asset_member m
		join asset_book b on b.tenant_id = m.tenant_id and b.id = m.asset_book_id
		join asset a on a.tenant_id = b.tenant_id and a.id = b.asset_id
		where m.tenant_id = $1 and m.group_id = $2 order by a.asset_number, a.id`, tenantID, id)
	if err != nil {
		return GroupAsset{}, err
	}
	defer rows.Close()
	g.Members = []GroupMember{}
	for rows.Next() {
		var m GroupMember
		if err := rows.Scan(&m.AssetID, &m.AssetNumber, &m.Name, &m.AssetBookID, &m.BookStatus, &m.InServiceDate, &m.Cost); err != nil {
			return GroupAsset{}, err
		}
		g.Members = append(g.Members, m)
	}
	if err := rows.Err(); err != nil {
		return GroupAsset{}, err
	}
	g.Schedule = []GroupYear{}
	if g.Basis.IsPositive() {
		years, err := domain.YearlySchedule(g.Profile())
		if err != nil {
			return GroupAsset{}, err
		}
		for _, y := range years {
			g.Schedule = append(g.Schedule, GroupYear{Year: y.Year, Amount: y.Amount, Accumulated: y.Accumulated, NBV: y.NBV})
		}
	}
	return g, nil
}

// groupMemberships maps every book with bookCode in the legal entity that
// belongs to a group asset to the group's code.
func groupMemberships(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, bookCode string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `select m.asset_book_id, g.group_code from group_asset_member m
		join group_asset g on g.tenant_id = m.tenant_id and g.id = m.group_id
		where m.tenant_id = $1 and g.legal_entity_id = $2 and g.book_code = $3`, tenantID, legalEntityID, bookCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var book, group string
		if err := rows.Scan(&book, &group); err != nil {
			return nil, err
		}
		out[book] = group
	}
	return out, rows.Err()
}

// groupLine is the run line of a member book: no own charge (FR-37.16). It is
// an exception until the group's own run line exists, so the books show in the
// run with the group that depreciates them.
func groupLine(b Book, group string) RunLine {
	code := domain.CodeDepreciatedAtGroup
	detail := "the book is depreciated at group asset " + group + "; it takes no own charge (the group's run line is not built yet)"
	return RunLine{AssetBookID: b.ID, AssetID: b.AssetID, Status: "Exception", Amount: decimal.Zero, ScheduledAmount: decimal.Zero,
		ExceptionCode: &code, Detail: &detail}
}
