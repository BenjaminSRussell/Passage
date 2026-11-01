package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

func containerIndex(sh Shipment, containerID string) int {
	for i, c := range sh.Containers {
		if c.ID == containerID {
			return i
		}
	}
	return -1
}

// NestContainers puts CLOSED cartons of a PICKED or PACKED shipment on a new
// pallet: the pallet gets its own SSCC, takes tare, returnable flag and
// material from a known container type, and weighs its own tare plus the
// cartons. It holds no lines, so the packed quantities do not change; the
// shipment version moves so a concurrent pack or unpack on the old version
// fails. Posting the packing slip ships the pallet with its cartons.
func (s *Postgres) NestContainers(ctx context.Context, id string, version int, containerType string, childIDs []string, idem Idem) (PackResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out PackResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (PackResult, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return PackResult{}, err
			}
			if err := checkVersion(sh, version); err != nil {
				return PackResult{}, err
			}
			children := make([]domain.NestChild, 0, len(childIDs))
			idxs := make([]int, 0, len(childIDs))
			for _, cid := range childIDs {
				i := containerIndex(sh, cid)
				if i < 0 {
					return PackResult{}, httpx.NewProblem(http.StatusNotFound, CodeContainerNotFound, "container not found on this shipment")
				}
				c := sh.Containers[i]
				idxs = append(idxs, i)
				children = append(children, domain.NestChild{SSCC: c.SSCC, Status: c.Status, OnPallet: c.ParentContainerID != nil, IsPallet: len(c.ChildContainerIDs) > 0})
			}
			if err := domain.NestContainers(sh.Status, children); err != nil {
				return PackResult{}, mapDomain(err)
			}
			pallet := Container{ID: ids.New(), ContainerType: containerType, Status: "CLOSED", Lines: []ContainerLine{}, ChildContainerIDs: []string{}}
			ct, known, err := loadContainerType(ctx, tx, containerType)
			if err != nil {
				return PackResult{}, err
			}
			if known {
				pallet.TareKg, pallet.Returnable, pallet.PackagingMaterial = ct.TareKg, ct.Returnable, ct.PackagingMaterial
			}
			pallet.GrossKg = pallet.TareKg
			for _, i := range idxs {
				pallet.GrossKg = pallet.GrossKg.Add(sh.Containers[i].GrossKg)
			}
			// Bump the shipment first (see UnpackContainer): a concurrent
			// command on the same version waits here and fails the check.
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, sh.Status); err != nil {
				return PackResult{}, err
			}
			if err := s.insertContainer(ctx, tx, p.TenantID, &sh, &pallet); err != nil {
				return PackResult{}, err
			}
			sh.Containers = sh.Containers[:len(sh.Containers)-1]
			ssccs := []string{}
			for _, i := range idxs {
				c := &sh.Containers[i]
				tag, err := tx.Exec(ctx, `update container set parent_container_id = $3 where tenant_id = $1 and id = $2 and status = 'CLOSED' and parent_container_id is null`,
					p.TenantID, c.ID, pallet.ID)
				if err != nil {
					return PackResult{}, err
				}
				if tag.RowsAffected() != 1 {
					return PackResult{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "container "+c.SSCC+" changed since the shipment was read; reload and retry")
				}
				pid := pallet.ID
				c.ParentContainerID = &pid
				pallet.ChildContainerIDs = append(pallet.ChildContainerIDs, c.ID)
				ssccs = append(ssccs, c.SSCC)
			}
			sh.Containers = append(sh.Containers, pallet)
			if err := enqueue(ctx, tx, sh.ID, fulevents.ContainerNested, p.TenantID, fulevents.ContainerNestedData{
				ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, ContainerID: pallet.ID, SSCC: pallet.SSCC,
				ContainerType: containerType, ChildSSCCs: ssccs}); err != nil {
				return PackResult{}, err
			}
			return PackResult{Container: pallet, Shipment: sh}, nil
		})
		out = res
		return err
	})
	return out, err
}

// UnnestContainer dissolves a pallet before it ships: the cartons go back to
// top-level containers and the pallet row stays with status UNPACKED so its SSCC
// is never issued again (BR-28.5). container.unnested lists the cartons.
func (s *Postgres) UnnestContainer(ctx context.Context, id string, version int, containerID string, idem Idem) (Shipment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Shipment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Shipment, error) {
			sh, err := loadShipment(ctx, tx, id)
			if err != nil {
				return sh, err
			}
			if err := checkVersion(sh, version); err != nil {
				return sh, err
			}
			idx := containerIndex(sh, containerID)
			if idx < 0 {
				return sh, httpx.NewProblem(http.StatusNotFound, CodeContainerNotFound, "container not found on this shipment")
			}
			pallet := sh.Containers[idx]
			if err := domain.UnnestPallet(sh.Status, pallet.Status, len(pallet.ChildContainerIDs)); err != nil {
				return sh, mapDomain(err)
			}
			if err := bumpShipment(ctx, tx, p.TenantID, &sh, sh.Status); err != nil {
				return sh, err
			}
			if _, err := tx.Exec(ctx, `update container set parent_container_id = null where tenant_id = $1 and parent_container_id = $2`,
				p.TenantID, pallet.ID); err != nil {
				return sh, err
			}
			if _, err := tx.Exec(ctx, `update container set status = 'UNPACKED', unpacked_at = now() where tenant_id = $1 and id = $2 and status = 'CLOSED'`,
				p.TenantID, pallet.ID); err != nil {
				return sh, err
			}
			ssccs := []string{}
			kept := make([]Container, 0, len(sh.Containers))
			for _, c := range sh.Containers {
				if c.ID == pallet.ID {
					continue
				}
				if c.ParentContainerID != nil && *c.ParentContainerID == pallet.ID {
					c.ParentContainerID = nil
					ssccs = append(ssccs, c.SSCC)
				}
				kept = append(kept, c)
			}
			sh.Containers = kept
			return sh, enqueue(ctx, tx, sh.ID, fulevents.ContainerUnnested, p.TenantID, fulevents.ContainerNestedData{
				ShipmentID: sh.ID, ShipmentNumber: sh.ShipmentNumber, ContainerID: pallet.ID, SSCC: pallet.SSCC,
				ContainerType: pallet.ContainerType, ChildSSCCs: ssccs})
		})
		out = res
		return err
	})
	return out, err
}
