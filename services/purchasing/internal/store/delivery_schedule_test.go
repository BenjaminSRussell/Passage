package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func remarshal(in map[string]any, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func sched(t *testing.T, rows ...string) []domain.ScheduleInput {
	t.Helper()
	var out []domain.ScheduleInput
	for i := 0; i+1 < len(rows); i += 2 {
		out = append(out, domain.ScheduleInput{Qty: dec(rows[i]), DeliveryDate: day(t, rows[i+1])})
	}
	return out
}

// FR-17.10 against Postgres: a 100-unit line takes a 60/40 schedule, which must add up to the
// line, is numbered in date order, follows a PO edit of the line quantity, rides on
// po.confirmed.v1 and is refused once the PO leaves DRAFT or REJECTED; the vendor's confirmed
// date is taken on the CONFIRMED PO, writes one event and is idempotent.
func TestDeliverySchedule_FR17_10(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(70)
	po, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "100", "2027-02-01"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetDeliverySchedule(ctx, po.ID, 1); problemCode(err) != store.CodeDeliveryScheduleNotFound {
		t.Fatalf("no schedule yet = %v", err)
	}
	if _, err := s.PutDeliverySchedule(ctx, po.ID, 1, po.RowVersion, sched(t, "60", "2027-02-01", "30", "2027-03-01")); problemCode(err) != domain.CodeScheduleSum {
		t.Fatalf("90 of 100 = %v, want %s", err, domain.CodeScheduleSum)
	}
	if _, err := s.PutDeliverySchedule(ctx, po.ID, 1, po.RowVersion+1, sched(t, "100", "2027-02-01")); problemCode(err) != store.CodeStaleVersion {
		t.Fatalf("stale If-Match = %v", err)
	}
	if _, err := s.PutDeliverySchedule(ctx, po.ID, 2, po.RowVersion, sched(t, "100", "2027-02-01")); problemCode(err) != store.CodePOLineNotFound {
		t.Fatalf("missing line = %v", err)
	}
	put, err := s.PutDeliverySchedule(ctx, po.ID, 1, po.RowVersion, sched(t, "40", "2027-03-01", "60", "2027-02-01"))
	if err != nil {
		t.Fatal(err)
	}
	if put.PORowVersion != po.RowVersion+1 || len(put.Rows) != 2 || put.Unit != "EA" || !put.LineQty.Equal(dec("100")) ||
		put.Rows[0].Seq != 1 || !put.Rows[0].Qty.Equal(dec("60")) || put.Rows[0].DeliveryDate.String() != "2027-02-01" || put.Rows[1].Seq != 2 {
		t.Fatalf("schedule = %+v", put)
	}
	list, err := s.ListDeliverySchedules(ctx, po.ID)
	if err != nil || len(list) != 1 || list[0].LineNo != 1 || len(list[0].Rows) != 2 {
		t.Fatalf("list = %+v (%v)", list, err)
	}
	if _, err := s.ListDeliverySchedules(ctx, ids.New()); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("list of an unknown PO = %v", err)
	}

	// Editing the line quantity takes the 10 units from the last row.
	cur, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "90", "2027-02-01"))); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDeliverySchedule(ctx, po.ID, 1)
	if err != nil || len(got.Rows) != 2 || !got.Rows[0].Qty.Equal(dec("60")) || !got.Rows[1].Qty.Equal(dec("30")) || !got.LineQty.Equal(dec("90")) {
		t.Fatalf("after the edit to 90 = %+v (%v)", got, err)
	}
	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	if _, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "50", "2027-02-01"))); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDeliverySchedule(ctx, po.ID, 1)
	if len(got.Rows) != 1 || !got.Rows[0].Qty.Equal(dec("50")) {
		t.Fatalf("after the edit to 50 = %+v", got)
	}

	// A confirmed date needs a CONFIRMED PO; the schedule is frozen once the PO leaves DRAFT.
	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	if _, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, 1, cur.RowVersion, day(t, "2027-02-05")); problemCode(err) != domain.CodeScheduleTarget {
		t.Fatalf("confirmed date on a DRAFT PO = %v", err)
	}
	cur = walk(t, ctx, s, cur, domain.POActionSubmit, domain.POActionApprove, domain.POActionConfirm)
	if _, err := s.PutDeliverySchedule(ctx, po.ID, 1, cur.RowVersion, sched(t, "50", "2027-02-01")); problemCode(err) != domain.CodeScheduleTarget {
		t.Fatalf("replace on a CONFIRMED PO = %v", err)
	}
	rows := outbox(t, ctx, pool)
	if types := eventTypes(rows); len(types) != 1 || types[0] != purchasingevents.POConfirmed {
		t.Fatalf("outbox after confirm = %v", types)
	}
	var confirmed purchasingevents.POConfirmedData
	if err := remarshal(rows[0].Data, &confirmed); err != nil || len(confirmed.Lines) != 1 || len(confirmed.Lines[0].Schedule) != 1 ||
		confirmed.Lines[0].Schedule[0].Qty != "50" || confirmed.Lines[0].Schedule[0].DeliveryDate != "2027-02-01" {
		t.Fatalf("po.confirmed lines = %+v (%v)", confirmed.Lines, err)
	}

	res, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, 1, cur.RowVersion, day(t, "2027-02-05"))
	if err != nil {
		t.Fatal(err)
	}
	if res.PORowVersion != cur.RowVersion+1 || res.Rows[0].ConfirmedDate == nil || res.Rows[0].ConfirmedDate.String() != "2027-02-05" || res.Rows[0].ConfirmedAt == nil {
		t.Fatalf("confirmed = %+v", res)
	}
	if _, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, 2, res.PORowVersion, day(t, "2027-02-05")); problemCode(err) != store.CodeDeliveryScheduleNotFound {
		t.Fatalf("unknown row = %v", err)
	}
	if _, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, 1, cur.RowVersion, day(t, "2027-02-06")); problemCode(err) != store.CodeStaleVersion {
		t.Fatalf("stale If-Match on confirm = %v", err)
	}
	again, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, 1, res.PORowVersion, day(t, "2027-02-05"))
	if err != nil || again.PORowVersion != res.PORowVersion {
		t.Fatalf("replay = %+v (%v)", again, err)
	}
	moved9, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, 1, res.PORowVersion, day(t, "2027-02-09"))
	if err != nil {
		t.Fatal(err)
	}
	// A retry of that call after a lost response carries the old ETag: still a replay, not a 412.
	if retry, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, 1, res.PORowVersion, day(t, "2027-02-09")); err != nil || retry.PORowVersion != moved9.PORowVersion {
		t.Fatalf("retry with the ETag before the change = %+v (%v)", retry, err)
	}
	var moved []string
	for _, r := range outbox(t, ctx, pool) {
		if r.Type == purchasingevents.DeliveryScheduleConfirmed {
			var data purchasingevents.DeliveryScheduleConfirmedData
			_ = remarshal(r.Data, &data)
			moved = append(moved, data.ConfirmedDate+"<-"+data.PreviousConfirmedDate)
		}
	}
	if len(moved) != 2 || moved[0] != "2027-02-05<-" || moved[1] != "2027-02-09<-2027-02-05" {
		t.Fatalf("deliveryschedule.confirmed events = %v (the replay must write none)", moved)
	}

	// Cancelling the PO removes the schedule with its lines.
	cur, _ = s.GetPurchaseOrder(ctx, po.ID)
	if _, err := s.ActOnPurchaseOrder(ctx, po.ID, cur.RowVersion, domain.POActionCancel, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDeliverySchedule(ctx, po.ID, 1); problemCode(err) != store.CodeDeliveryScheduleNotFound {
		t.Fatalf("schedule of a cancelled PO = %v", err)
	}
}

func TestDeliverySchedule_DeleteAndRewrite(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(71)
	po, err := s.CreatePurchaseOrder(ctx, lifecyclePO(t, ids.New(), "10", "2027-02-01"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteDeliverySchedule(ctx, po.ID, 1, po.RowVersion); problemCode(err) != store.CodeDeliveryScheduleNotFound {
		t.Fatalf("delete of a missing schedule = %v", err)
	}
	put, err := s.PutDeliverySchedule(ctx, po.ID, 1, po.RowVersion, sched(t, "4", "2027-02-01", "6", "2027-02-08"))
	if err != nil {
		t.Fatal(err)
	}
	put, err = s.PutDeliverySchedule(ctx, po.ID, 1, put.PORowVersion, sched(t, "10", "2027-02-15"))
	if err != nil || len(put.Rows) != 1 || !put.Rows[0].Qty.Equal(dec("10")) {
		t.Fatalf("rewrite = %+v (%v)", put, err)
	}
	v, err := s.DeleteDeliverySchedule(ctx, po.ID, 1, put.PORowVersion)
	if err != nil || v != put.PORowVersion+1 {
		t.Fatalf("delete = %d (%v)", v, err)
	}
	if list, _ := s.ListDeliverySchedules(ctx, po.ID); len(list) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}
}

// A line edit of a confirmed PO moves the difference to the last row, which loses the vendor's
// confirmed date (the vendor confirmed another quantity); the first row keeps its date, and the
// po.confirmed.v1 of the change carries both.
func TestDeliverySchedule_EditOfAConfirmedPOClearsTheChangedRowsConfirmation(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(72)
	in := lifecyclePO(t, ids.New(), "100", "2027-02-01")
	in.ChangeManagement = false
	po, err := s.CreatePurchaseOrder(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	put, err := s.PutDeliverySchedule(ctx, po.ID, 1, po.RowVersion, sched(t, "60", "2027-02-01", "40", "2027-03-01"))
	if err != nil {
		t.Fatal(err)
	}
	po.RowVersion = put.PORowVersion
	po = walk(t, ctx, s, po, domain.POActionSubmit, domain.POActionApprove, domain.POActionConfirm)
	v := po.RowVersion
	for seq, date := range map[int]string{1: "2027-02-02", 2: "2027-03-02"} {
		res, err := s.ConfirmDeliveryDate(ctx, po.ID, 1, seq, v, day(t, date))
		if err != nil {
			t.Fatal(err)
		}
		v = res.PORowVersion
	}
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	next, err := s.EditPurchaseOrder(ctx, po.ID, cur.RowVersion, edited(cur, lifecyclePO(t, "", "120", "2027-02-01")))
	if err != nil || next.ApprovalStatus != domain.POStatusConfirmed || next.VersionNo != 2 {
		t.Fatalf("edit = %s v%d (%v)", next.ApprovalStatus, next.VersionNo, err)
	}
	got, err := s.GetDeliverySchedule(ctx, po.ID, 1)
	if err != nil || len(got.Rows) != 2 || !got.Rows[1].Qty.Equal(dec("60")) {
		t.Fatalf("after the edit to 120 = %+v (%v)", got, err)
	}
	if got.Rows[0].ConfirmedDate == nil || got.Rows[0].ConfirmedDate.String() != "2027-02-02" || got.Rows[1].ConfirmedDate != nil || got.Rows[1].ConfirmedAt != nil {
		t.Fatalf("confirmations after the edit = %+v", got.Rows)
	}
	var last purchasingevents.POConfirmedData
	for _, r := range outbox(t, ctx, pool) {
		if r.Type == purchasingevents.POConfirmed {
			if err := remarshal(r.Data, &last); err != nil {
				t.Fatal(err)
			}
		}
	}
	if sc := last.Lines[0].Schedule; last.VersionNo != 2 || len(sc) != 2 || sc[0].ConfirmedDate != "2027-02-02" || sc[1].ConfirmedDate != "" || sc[1].Qty != "60" {
		t.Fatalf("po.confirmed v%d schedule = %+v", last.VersionNo, last.Lines[0].Schedule)
	}
}

// po.confirmed.v1 carries every schedule row, so one PO holds at most MaxScheduleRowsPerPO of them
// (PRC-DS-003); replacing a line's own rows does not count them twice.
func TestDeliverySchedule_RowBudgetPerPO(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(73)
	in := lifecyclePO(t, ids.New(), "100", "2027-02-01")
	base := in.Lines[0]
	lines := domain.MaxScheduleRowsPerPO/domain.MaxScheduleRows + 1
	for n := 2; n <= lines; n++ {
		l := base
		l.LineNo = n
		in.Lines = append(in.Lines, l)
	}
	total := base.LineAmount.Mul(dec(strconv.Itoa(lines)))
	in.Subtotal, in.Total = total, total
	po, err := s.CreatePurchaseOrder(ctx, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	hundred := make([]string, 0, 2*domain.MaxScheduleRows)
	for i := 0; i < domain.MaxScheduleRows; i++ {
		hundred = append(hundred, "1", "2027-02-01")
	}
	v := po.RowVersion
	for n := 1; n < lines; n++ {
		res, err := s.PutDeliverySchedule(ctx, po.ID, n, v, sched(t, hundred...))
		if err != nil {
			t.Fatalf("line %d: %v", n, err)
		}
		v = res.PORowVersion
	}
	res, err := s.PutDeliverySchedule(ctx, po.ID, 1, v, sched(t, hundred...))
	if err != nil {
		t.Fatalf("replacing a full line's rows: %v", err)
	}
	if _, err := s.PutDeliverySchedule(ctx, po.ID, lines, res.PORowVersion, sched(t, "60", "2027-02-01", "40", "2027-03-01")); problemCode(err) != domain.CodeScheduleBudget {
		t.Fatalf("row %d of the PO = %v, want %s", domain.MaxScheduleRowsPerPO+1, err, domain.CodeScheduleBudget)
	}
}

// The schedule table's own policies (0018) hide another legal entity's rows and refuse writing
// under another entity's PO, not only the store's PO visibility check.
func TestDeliverySchedule_LegalEntityScopeOnTheTable(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	entityA, entityB := ids.New(), ids.New()
	ctxA := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(74), LegalEntityID: entityA, UserID: ids.New()})
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(74), LegalEntityID: entityB, UserID: ids.New()})
	in := lifecyclePO(t, ids.New(), "10", "2027-02-01")
	in.LegalEntityID = entityA
	po, err := s.CreatePurchaseOrder(ctxA, in, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDeliverySchedule(ctxA, po.ID, 1, po.RowVersion, sched(t, "4", "2027-02-01", "6", "2027-02-08")); err != nil {
		t.Fatal(err)
	}
	count := func(ctx context.Context) int {
		var n int
		if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from purchase_delivery_schedule where purchase_order_id = $1`, po.ID).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if a, b := count(ctxA), count(ctxB); a != 2 || b != 0 {
		t.Fatalf("rows seen by entity A = %d (want 2), by entity B = %d (want 0)", a, b)
	}
	if _, err := s.GetDeliverySchedule(ctxB, po.ID, 1); problemCode(err) != store.CodePurchaseOrderNotFound {
		t.Fatalf("entity B read = %v", err)
	}
	err = dbx.WithTenantTx(ctxB, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctxB, `insert into purchase_delivery_schedule (tenant_id, purchase_order_id, line_no, seq, qty, delivery_date)
			values ($1, $2, 1, 99, 1, '2027-02-01')`, testkit.TenantID(74), po.ID)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("insert under entity A's PO from entity B = %v, want 42501", err)
	}
}
