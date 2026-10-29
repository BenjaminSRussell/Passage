package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	assetevents "github.com/BenjaminSRussell/passage/services/assets/internal/events"
)

// TransferPart is one target asset of a transfer or split. LegalEntityID,
// ClassCode and Quantity default to the source asset's (a split's quantity
// defaults to the source quantity times the share).
type TransferPart struct {
	AssetNumber   string           `json:"assetNumber"`
	Name          string           `json:"name"`
	Percentage    decimal.Decimal  `json:"percentage"`
	LegalEntityID string           `json:"legalEntityId"`
	ClassCode     string           `json:"classCode"`
	Barcode       *string          `json:"barcode"`
	Quantity      *decimal.Decimal `json:"quantity"`
}

// TransferInput is a validated transfer or split of a whole asset (FR-37.7).
// Version is the asset version the caller read (If-Match).
type TransferInput struct {
	Version int                 `json:"version"`
	Kind    domain.TransferKind `json:"kind"`
	Date    Date                `json:"transferDate"`
	Parts   []TransferPart      `json:"parts"`
}

// TransferTarget is one asset a transfer or split created, the share it took
// and its books.
type TransferTarget struct {
	LineNo     int             `json:"lineNo"`
	Percentage decimal.Decimal `json:"percentage"`
	Asset      Asset           `json:"asset"`
	Books      []Book          `json:"books"`
}

// Transfer is one transfer or split: the source asset after it (Transferred,
// every book emptied), the targets that received its balances, and the
// transactions written on the source and target books.
type Transfer struct {
	ID            string              `json:"id"`
	Kind          domain.TransferKind `json:"kind"`
	TransferDate  Date                `json:"transferDate"`
	LegalEntityID string              `json:"legalEntityId"`
	SourceAssetID string              `json:"sourceAssetId"`
	SourceNumber  string              `json:"sourceAssetNumber"`
	CreatedBy     *string             `json:"createdBy"`
	CreatedAt     time.Time           `json:"createdAt"`
	Source        *Asset              `json:"source,omitempty"`
	Targets       []TransferTarget    `json:"targets"`
	Transactions  []Transaction       `json:"transactions,omitempty"`
}

// Transfer moves every Open book of an Open asset into one new asset
// (transfer) or divides it into several (split), FR-37.7. Cost, accumulated
// depreciation, the impairment share, salvage and the schedule basis are
// allocated by percentage with the residual cents on the last target
// (BR-37.7); each target book keeps the source profile, in-service date and
// last depreciated period, so its schedule continues where the source stopped.
// Every source book is emptied by a Transfer / Split transaction and marked
// Transferred, the source asset becomes Transferred (BR-37.2), and one
// asset.transferred or asset.split event is written in the same transaction.
func (s *Postgres) Transfer(ctx context.Context, assetID string, in TransferInput, idempotencyKey string) (Transfer, error) {
	p := principal(ctx)
	pcts := make([]decimal.Decimal, len(in.Parts))
	for i, part := range in.Parts {
		pcts[i] = part.Percentage
	}
	if err := domain.ValidateTransfer(in.Kind, pcts); err != nil {
		return Transfer{}, err
	}
	var out Transfer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		request := struct {
			AssetID string
			Input   TransferInput
		}{assetID, in}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, idempotencyKey, "asset."+string(in.Kind), request, func() (Transfer, error) {
			return s.transfer(ctx, tx, p.TenantID, p.UserID, assetID, in, pcts)
		})
		return err
	})
	if c := uniqueConstraint(err); c != "" {
		if c == "asset_barcode_unique" {
			return Transfer{}, httpx.NewProblem(http.StatusConflict, CodeAssetNumberDup, "a target barcode already exists in its legal entity")
		}
		return Transfer{}, httpx.NewProblem(http.StatusConflict, CodeAssetNumberDup, "a target asset number already exists in its legal entity")
	}
	return out, err
}

func (s *Postgres) transfer(ctx context.Context, tx pgx.Tx, tenantID, userID, assetID string, in TransferInput, pcts []decimal.Decimal) (Transfer, error) {
	src, err := scanAsset(tx.QueryRow(ctx, `select `+assetColumns+` from asset a where a.tenant_id = $1 and a.id = $2 for update`, tenantID, assetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, notFound(CodeAssetNotFound, "no asset with id "+assetID)
	}
	if err != nil {
		return Transfer{}, err
	}
	if src.Version != in.Version {
		return Transfer{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
			fmt.Sprintf("the asset changed since you read it (version %d, current %d); reload and try again", in.Version, src.Version))
	}
	if src.Status != string(domain.StatusOpen) {
		return Transfer{}, &domain.Error{Code: domain.CodeAssetState, Detail: "asset is " + src.Status + "; only an Open asset can be transferred or split"}
	}
	all, err := loadBooks(ctx, tx, tenantID, assetID, true)
	if err != nil {
		return Transfer{}, err
	}
	var books []Book
	for _, b := range all {
		if b.Status == string(domain.StatusOpen) {
			books = append(books, b)
		}
	}
	if len(books) == 0 {
		return Transfer{}, &domain.Error{Code: domain.CodeAssetState, Detail: "the asset has no Open book to transfer"}
	}
	// Every Open book moves; allocate them all before writing anything.
	shares := make([][]domain.BookAmounts, len(books))
	for i, b := range books {
		if err := domain.CheckTransferDate(in.Date.Time, b.InServiceDate.Time, deref(b.LastDepreciatedPeriod)); err != nil {
			return Transfer{}, err
		}
		var impairment decimal.Decimal
		if err := tx.QueryRow(ctx, `select coalesce(sum(impairment_amount), 0) from asset_transaction where tenant_id = $1 and asset_book_id = $2`,
			tenantID, b.ID).Scan(&impairment); err != nil {
			return Transfer{}, err
		}
		if shares[i], err = domain.AllocateShares(domain.BookAmounts{Cost: b.Cost, Accumulated: b.AccumulatedDepreciation, Impairment: impairment,
			Salvage: b.SalvageValue, AcquisitionCost: b.AcquisitionCost}, pcts, b.Currency); err != nil {
			return Transfer{}, err
		}
	}

	transferID := ids.New()
	kind := string(in.Kind)
	txType := in.Kind.TransactionType()
	if _, err := tx.Exec(ctx, `insert into asset_transfer (tenant_id, id, legal_entity_id, source_asset_id, kind, transfer_date, created_by)
		values ($1, $2, $3, $4, $5, $6, nullif($7, ''))`, tenantID, transferID, src.LegalEntityID, assetID, kind, in.Date, userID); err != nil {
		return Transfer{}, err
	}
	res := Transfer{ID: transferID, Kind: in.Kind, TransferDate: in.Date, LegalEntityID: src.LegalEntityID, SourceAssetID: assetID,
		SourceNumber: src.AssetNumber, Targets: []TransferTarget{}, Transactions: []Transaction{}}
	evt := assetevents.AssetTransferredData{TransferID: transferID, SourceAssetID: assetID, LegalEntityID: src.LegalEntityID, Kind: kind,
		TransferDate: in.Date.String(), Targets: []assetevents.TransferTargetData{}, Books: []assetevents.TransferBookData{}}

	// Empty every source book first, so the source transactions come first in
	// the asset's history.
	for j, b := range books {
		impairment := decimal.Zero
		for _, sh := range shares[j] {
			impairment = impairment.Add(sh.Impairment)
		}
		t, err := postTransaction(ctx, tx, tenantID, b, txRow{Type: txType, Date: in.Date, Cost: b.Cost.Neg(), Accumulated: b.AccumulatedDepreciation.Neg(),
			Impairment: impairment.Neg(), SourceType: kind, SourceID: &transferID})
		if err != nil {
			return Transfer{}, err
		}
		res.Transactions = append(res.Transactions, t)
		if _, err := tx.Exec(ctx, `update asset_book set cost = 0, accumulated_depreciation = 0, nbv = 0, salvage_value = 0, acquisition_cost = 0,
			status = $3 where tenant_id = $1 and id = $2`, tenantID, b.ID, string(domain.StatusTransferred)); err != nil {
			return Transfer{}, err
		}
		evt.Books = append(evt.Books, assetevents.TransferBookData{BookCode: b.BookCode, Currency: b.Currency, Cost: b.Cost.String(),
			AccumulatedDepreciation: b.AccumulatedDepreciation.String(), NBV: b.NBV.String()})
	}

	for i, part := range in.Parts {
		legalEntity := src.LegalEntityID
		if part.LegalEntityID != "" && part.LegalEntityID != src.LegalEntityID {
			if in.Kind == domain.TransferKindSplit {
				return Transfer{}, &domain.Error{Code: domain.CodeInvalidTransfer, Detail: "a split stays in the source legal entity; transfer the asset to move it"}
			}
			legalEntity = part.LegalEntityID
		}
		class := src.ClassCode
		if part.ClassCode != "" {
			class = part.ClassCode
		}
		quantity := src.Quantity
		if part.Quantity != nil {
			quantity = *part.Quantity
		} else if in.Kind == domain.TransferKindSplit {
			quantity = src.Quantity.Mul(part.Percentage).Div(decimal.NewFromInt(100)).Round(4)
		}
		if !quantity.IsPositive() {
			return Transfer{}, &domain.Error{Code: domain.CodeInvalidTransfer, Detail: "target " + part.AssetNumber + " would get a zero quantity; give its quantity"}
		}
		targetID := ids.New()
		if _, err := tx.Exec(ctx, `insert into asset (tenant_id, id, legal_entity_id, asset_number, name, class_code, barcode, quantity, status, acquired_date)
			values ($1, $2, $3, $4, $5, $6, $7, $8, 'Open', $9)`,
			tenantID, targetID, legalEntity, part.AssetNumber, part.Name, class, part.Barcode, quantity, src.AcquiredDate); err != nil {
			return Transfer{}, err
		}
		if _, err := tx.Exec(ctx, `insert into asset_transfer_part (tenant_id, transfer_id, line_no, target_asset_id, legal_entity_id, percentage)
			values ($1, $2, $3, $4, $5, $6)`, tenantID, transferID, i+1, targetID, legalEntity, part.Percentage); err != nil {
			return Transfer{}, err
		}
		target := TransferTarget{LineNo: i + 1, Percentage: part.Percentage, Books: []Book{}}
		for j, b := range books {
			sh := shares[j][i]
			nb, err := scanBook(tx.QueryRow(ctx, `insert into asset_book (tenant_id, id, asset_id, legal_entity_id, book_code, currency, method, factor,
				switch_to_straight_line, service_life_periods, salvage_value, convention, in_service_date, cost, acquisition_cost, accumulated_depreciation, nbv,
				status, last_depreciated_period)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, 'Open', $18) returning `+bookColumns,
				tenantID, ids.New(), targetID, legalEntity, b.BookCode, b.Currency, b.Method, b.Factor, b.SwitchToStraightLine, b.ServiceLifePeriods,
				sh.Salvage, b.Convention, b.InServiceDate, sh.Cost, sh.AcquisitionCost, sh.Accumulated, sh.Cost.Sub(sh.Accumulated), b.LastDepreciatedPeriod))
			if err != nil {
				return Transfer{}, err
			}
			t, err := postTransaction(ctx, tx, tenantID, nb, txRow{Type: txType, Date: in.Date, Cost: sh.Cost, Accumulated: sh.Accumulated,
				Impairment: sh.Impairment, SourceType: kind, SourceID: &transferID})
			if err != nil {
				return Transfer{}, err
			}
			res.Transactions = append(res.Transactions, t)
			target.Books = append(target.Books, nb)
		}
		if target.Asset, err = loadAsset(ctx, tx, tenantID, targetID); err != nil {
			return Transfer{}, err
		}
		res.Targets = append(res.Targets, target)
		evt.Targets = append(evt.Targets, assetevents.TransferTargetData{AssetID: targetID, AssetNumber: part.AssetNumber, LegalEntityID: legalEntity,
			Percentage: part.Percentage.StringFixed(2)})
	}

	if _, err := tx.Exec(ctx, `update asset set status = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
		tenantID, assetID, string(domain.StatusTransferred)); err != nil {
		return Transfer{}, err
	}
	source, err := loadAsset(ctx, tx, tenantID, assetID)
	if err != nil {
		return Transfer{}, err
	}
	res.Source = &source
	if userID != "" {
		res.CreatedBy = &userID
	}
	if err := tx.QueryRow(ctx, `select created_at from asset_transfer where tenant_id = $1 and id = $2`, tenantID, transferID).Scan(&res.CreatedAt); err != nil {
		return Transfer{}, err
	}
	eventType := assetevents.AssetTransferred
	if in.Kind == domain.TransferKindSplit {
		eventType = assetevents.AssetSplit
	}
	return res, enqueue(ctx, tx, "asset", assetID, eventType, tenantID, evt)
}

// ListTransfers returns the transfers and splits an asset took part in, as
// source or as target, oldest first, with their targets (no books).
func (s *Postgres) ListTransfers(ctx context.Context, assetID string) ([]Transfer, error) {
	p := principal(ctx)
	out := []Transfer{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadAsset(ctx, tx, p.TenantID, assetID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select t.id, t.kind, t.transfer_date, t.legal_entity_id, t.source_asset_id, a.asset_number, t.created_by, t.created_at
			from asset_transfer t join asset a on a.tenant_id = t.tenant_id and a.id = t.source_asset_id
			where t.tenant_id = $1 and (t.source_asset_id = $2 or exists (
				select 1 from asset_transfer_part pt where pt.tenant_id = t.tenant_id and pt.transfer_id = t.id and pt.target_asset_id = $2))
			order by t.transfer_date, t.created_at`, p.TenantID, assetID)
		if err != nil {
			return err
		}
		index := map[string]int{}
		for rows.Next() {
			var t Transfer
			if err := rows.Scan(&t.ID, &t.Kind, &t.TransferDate, &t.LegalEntityID, &t.SourceAssetID, &t.SourceNumber, &t.CreatedBy, &t.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			t.Targets = []TransferTarget{}
			index[t.ID] = len(out)
			out = append(out, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(out) == 0 {
			return err
		}
		transferIDs := make([]string, 0, len(out))
		for _, t := range out {
			transferIDs = append(transferIDs, t.ID)
		}
		// A target in a legal entity outside the caller's scope is hidden by
		// row-level security; the transfer itself still lists.
		parts, err := tx.Query(ctx, `select pt.transfer_id, pt.line_no, pt.percentage, `+assetColumns+`
			from asset_transfer_part pt join asset a on a.tenant_id = pt.tenant_id and a.id = pt.target_asset_id
			where pt.tenant_id = $1 and pt.transfer_id::text = any($2) order by pt.transfer_id, pt.line_no`, p.TenantID, transferIDs)
		if err != nil {
			return err
		}
		defer parts.Close()
		for parts.Next() {
			var transferID string
			var tt TransferTarget
			a := &tt.Asset
			if err := parts.Scan(&transferID, &tt.LineNo, &tt.Percentage, &a.ID, &a.LegalEntityID, &a.AssetNumber, &a.Name, &a.ClassCode, &a.Status,
				&a.Barcode, &a.Quantity, &a.AcquiredDate, &a.BookCount, &a.Version, &a.CreatedAt); err != nil {
				return err
			}
			tt.Books = []Book{}
			i := index[transferID]
			out[i].Targets = append(out[i].Targets, tt)
		}
		return parts.Err()
	})
	return out, err
}
