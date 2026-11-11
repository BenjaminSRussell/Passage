package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// CaptureInput is an image a device uploads for a work order: a photo or the customer's
// signature (FR-45.8). The id is the device's, so a retried upload stores nothing twice.
type CaptureInput struct {
	ID           string
	WorkOrderID  string
	TechnicianID string
	Kind         domain.CaptureKind
	ContentType  string
	Width        int
	Height       int
	Data         []byte
	CapturedAt   time.Time
	Latitude     *float64
	Longitude    *float64
}

// Capture is the stored description of an upload; Ref is what an action names it by.
type Capture struct {
	ID           string    `json:"id"`
	Ref          string    `json:"ref"`
	WorkOrderID  string    `json:"workOrderId"`
	TechnicianID string    `json:"technicianId"`
	Kind         string    `json:"kind"`
	ContentType  string    `json:"contentType"`
	WidthPx      int       `json:"widthPx"`
	HeightPx     int       `json:"heightPx"`
	ByteSize     int       `json:"byteSize"`
	SHA256       string    `json:"sha256"`
	CapturedAt   time.Time `json:"capturedAt"`
	Latitude     *float64  `json:"latitude,omitempty"`
	Longitude    *float64  `json:"longitude,omitempty"`
	Duplicate    bool      `json:"duplicate"`
}

const captureColumns = `id::text, work_order_id::text, technician_id::text, kind, content_type, width_px, height_px, byte_size, sha256, captured_at, lat::float8, lon::float8`

func scanCapture(row pgx.Row, data *[]byte) (Capture, error) {
	var c Capture
	dest := []any{&c.ID, &c.WorkOrderID, &c.TechnicianID, &c.Kind, &c.ContentType, &c.WidthPx, &c.HeightPx, &c.ByteSize, &c.SHA256, &c.CapturedAt, &c.Latitude, &c.Longitude}
	if data != nil {
		dest = append(dest, data)
	}
	err := row.Scan(dest...)
	c.Ref = domain.CaptureRef(c.ID)
	return c, err
}

// PutCapture stores an uploaded image. The technician must be the caller's (or the caller
// office staff, as for actions) and the work order assigned to them and being worked on
// (dispatched up to completed). An id uploaded again with the same bytes returns the stored
// description with Duplicate set; with other bytes, or for another work order, it is refused
// with SVC-SYNC-005.
func (s *Postgres) PutCapture(ctx context.Context, in CaptureInput) (Capture, error) {
	tid := tenantOf(ctx)
	sum := sha256.Sum256(in.Data)
	hash := hex.EncodeToString(sum[:])
	var out Capture
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		wo, err := lockWorkOrder(ctx, tx, tid, in.WorkOrderID)
		if err != nil {
			return err
		}
		tech, err := getTechnician(ctx, tx, tid, in.TechnicianID, false)
		if err != nil {
			return err
		}
		if err := actingFor(ctx, tech); err != nil {
			return err
		}
		if wo.TechnicianID == nil || *wo.TechnicianID != in.TechnicianID {
			return httpx.NewProblem(http.StatusForbidden, domain.CodeActionTechnician, "the work order is not assigned to this technician")
		}
		existing, err := scanCapture(tx.QueryRow(ctx, `select `+captureColumns+` from service_wo_capture where tenant_id = $1 and id = $2`, tid, in.ID), nil)
		if err == nil {
			if existing.WorkOrderID != in.WorkOrderID || existing.TechnicianID != in.TechnicianID || existing.Kind != string(in.Kind) || existing.SHA256 != hash {
				return unprocessable(domain.CodeActionReuse, fmt.Sprintf("the capture id %s was already used for another image", in.ID))
			}
			existing.Duplicate = true
			out = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch domain.WOStatus(wo.Status) {
		case domain.WODispatched, domain.WOTraveling, domain.WOInProgress, domain.WOOnHold, domain.WOCompleted:
		default:
			return unprocessable(domain.CodeCaptureState, fmt.Sprintf("a %s work order takes no captures", wo.Status))
		}
		if _, err := tx.Exec(ctx, `insert into service_wo_capture (tenant_id, id, work_order_id, technician_id, legal_entity_id, kind, content_type, width_px, height_px,
			byte_size, sha256, data, captured_at, lat, lon) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			tid, in.ID, in.WorkOrderID, in.TechnicianID, wo.LegalEntityID, string(in.Kind), in.ContentType, in.Width, in.Height, len(in.Data), hash, in.Data,
			in.CapturedAt, in.Latitude, in.Longitude); err != nil {
			if isUnique(err) {
				return unprocessable(domain.CodeActionReuse, fmt.Sprintf("the capture id %s was already used for another image", in.ID))
			}
			return err
		}
		out, err = scanCapture(tx.QueryRow(ctx, `select `+captureColumns+` from service_wo_capture where tenant_id = $1 and id = $2`, tid, in.ID), nil)
		return err
	})
	return out, err
}

// GetCapture returns the description and bytes of one capture of a work order.
func (s *Postgres) GetCapture(ctx context.Context, workOrderID, id string) (Capture, []byte, error) {
	tid := tenantOf(ctx)
	var c Capture
	var data []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		c, err = scanCapture(tx.QueryRow(ctx, `select `+captureColumns+`, data from service_wo_capture where tenant_id = $1 and work_order_id = $2 and id = $3`,
			tid, workOrderID, id), &data)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil, httpx.NewProblem(http.StatusNotFound, domain.CodeCaptureUnknown, "capture not found")
	}
	return c, data, err
}

// SetChecklist replaces the checklist of a work order that has not been completed. Items
// already ticked on the device keep their state by id.
func (s *Postgres) SetChecklist(ctx context.Context, id string, version int, items []domain.ChecklistItem) (WorkOrder, error) {
	tid := tenantOf(ctx)
	var wo WorkOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockWorkOrder(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return versionConflict(current.Version)
		}
		switch domain.WOStatus(current.Status) {
		case domain.WOCompleted, domain.WOPosted, domain.WOCanceled:
			return unprocessable(CodeWOInvalid, fmt.Sprintf("the checklist of a %s work order cannot change", current.Status))
		}
		b, err := marshalJSON(items)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update service_work_order set checklist = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, tid, id, b); err != nil {
			return err
		}
		wo, err = getWorkOrder(ctx, tx, tid, id)
		return err
	})
	return wo, err
}
