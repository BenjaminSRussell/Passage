package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Control recipe problem codes (see README).
const (
	CodeRecipeNotFound = "SF-0174" // 404
	// CodeRecipeTransition is a status the recipe cannot move to from where it is (409), or a resend of a
	// recipe that is not in error.
	CodeRecipeTransition = "SF-0175"
	// CodeResendLimit is a resend past the fifth (422, BR-25.11).
	CodeResendLimit = "SF-0176"
	// CodeResendForce is a resend after process messages for the order were received, without force and
	// a reason (422, BR-25.11).
	CodeResendForce = "SF-0177"
	// CodeRecipeDestination is a destination the recipe cannot go to: a process control destination
	// without a usable edge node, a status report for the wrong destination, or a write that matches no
	// setpoint of the recipe (422).
	CodeRecipeDestination = "SF-0178"

	recipeSource = "controlrecipe"
	// ReadbackTolerance is the absolute difference between the value written and the value read back that
	// still counts as a match for a Double node or one of unknown type; domain.ReadbackTolerance gives it by node type.
	ReadbackTolerance = domain.DefaultReadbackTolerance
)

// ControlRecipe is one generated recipe with its tracking state (the control recipe monitor row).
type ControlRecipe struct {
	ID          string          `json:"id"`
	OrderID     string          `json:"orderId"`
	ResourceID  string          `json:"resourceId"`
	Destination string          `json:"destination"`
	EdgeID      *string         `json:"edgeId"`
	Topic       *string         `json:"topic"`
	Status      string          `json:"status"`
	Recipe      json.RawMessage `json:"recipe"`
	SendCount   int             `json:"sendCount"`
	LastSentAt  time.Time       `json:"lastSentAt"`
	LastError   *string         `json:"lastError"`
	Version     int             `json:"version"`
	CreatedBy   string          `json:"createdBy"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// RecipeEvent is one entry of a recipe's history.
type RecipeEvent struct {
	ID         string    `json:"id"`
	FromStatus *string   `json:"fromStatus"`
	ToStatus   string    `json:"toStatus"`
	Actor      string    `json:"actor"`
	Note       *string   `json:"note"`
	Forced     bool      `json:"forced"`
	At         time.Time `json:"at"`
}

// WriteAudit is one setpoint write the connector made or refused, with the value read back (BR-25.15).
type WriteAudit struct {
	ID        string           `json:"id"`
	PhasePath string           `json:"phasePath"`
	Node      string           `json:"node"`
	Value     decimal.Decimal  `json:"value"`
	Readback  *decimal.Decimal `json:"readback"`
	Written   bool             `json:"written"`
	Deviation bool             `json:"deviation"`
	Reason    *string          `json:"reason"`
	At        time.Time        `json:"at"`
}

// ControlRecipeDetail is a recipe with its history and write audit.
type ControlRecipeDetail struct {
	ControlRecipe
	History []RecipeEvent `json:"history"`
	Writes  []WriteAudit  `json:"writes"`
}

// ControlRecipeInput is a recipe to generate and send.
type ControlRecipeInput struct {
	OrderID     string
	ResourceID  string
	Destination string
	EdgeID      *string
	Topic       *string
	Recipe      domain.RecipeDoc
}

// RecipeFilter selects monitor rows; empty fields match anything.
type RecipeFilter struct {
	Status, Destination, OrderID, ResourceID, EdgeID string
	Limit                                            int
}

// EdgeWrite is one write the connector reports.
type EdgeWrite struct {
	PhasePath string
	Node      string
	Value     decimal.Decimal
	Readback  *decimal.Decimal
	// NodeType is the data type of the node the connector reports (Double, Float, Int32, Boolean, ...), "" when unknown.
	NodeType string
	Written  bool
	Reason   *string
	At       time.Time
}

// EdgeRecipeReport is what the connector reports for a recipe: an optional status and the writes it made
// or refused.
type EdgeRecipeReport struct {
	Status string
	Error  *string
	Writes []EdgeWrite
}

const recipeColumns = `r.id::text, r.order_id::text, r.resource_id::text, r.destination, r.edge_id::text, r.topic, r.status, r.recipe,
	r.send_count, r.last_sent_at, r.last_error, r.version, r.created_by, r.created_at, r.updated_at`

func scanRecipe(row pgx.Row) (ControlRecipe, error) {
	var c ControlRecipe
	err := row.Scan(&c.ID, &c.OrderID, &c.ResourceID, &c.Destination, &c.EdgeID, &c.Topic, &c.Status, &c.Recipe,
		&c.SendCount, &c.LastSentAt, &c.LastError, &c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func recipeNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeRecipeNotFound, "control recipe not found")
}

// recordRecipeChange writes the history row and the statuschanged event of one change.
func recordRecipeChange(ctx context.Context, tx pgx.Tx, tenantID string, c ControlRecipe, from *string, actor string, note *string, forced bool) error {
	if _, err := tx.Exec(ctx, `insert into control_recipe_event (tenant_id, id, recipe_id, from_status, to_status, actor, note, forced)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`, tenantID, ids.New(), c.ID, from, c.Status, actor, note, forced); err != nil {
		return err
	}
	return enqueue(ctx, tx, recipeSource, c.ID, sfevents.ControlRecipeStatusChanged, tenantID, sfevents.ControlRecipeStatusChangedData{
		RecipeID: c.ID, OrderID: c.OrderID, ResourceID: c.ResourceID, Destination: c.Destination, From: from, To: c.Status,
		SendCount: c.SendCount, Forced: forced, Note: note, Actor: actor})
}

// CreateControlRecipe stores a recipe and sends it: the kiosk finds it by resource, a connector by its
// edge node. A process control destination needs an edge node that exists and is switched on (SF-0178).
// Idempotent by the Idempotency-Key.
func (s *Postgres) CreateControlRecipe(ctx context.Context, in ControlRecipeInput, createdBy string, idem Idem) (ControlRecipe, error) {
	var out ControlRecipe
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = createControlRecipeTx(ctx, tx, in, nil, createdBy, idem)
		return err
	})
	return out, err
}

// createControlRecipeTx stores and sends a recipe inside tx; piSheetID names the PI sheet it was made from, if any.
func createControlRecipeTx(ctx context.Context, tx pgx.Tx, in ControlRecipeInput, piSheetID *string, createdBy string, idem Idem) (ControlRecipe, error) {
	p, _ := tenant.FromContext(ctx)
	doc, err := json.Marshal(in.Recipe)
	if err != nil {
		return ControlRecipe{}, err
	}
	if in.EdgeID != nil {
		var active bool
		err := tx.QueryRow(ctx, `select active from edge_node where tenant_id = $1 and id = $2`, p.TenantID, *in.EdgeID).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return ControlRecipe{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecipeDestination, "the edge node does not exist")
		}
		if err != nil {
			return ControlRecipe{}, err
		}
		if !active {
			return ControlRecipe{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecipeDestination, "the edge node is switched off")
		}
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into control_recipe (tenant_id, id, order_id, resource_id, destination, edge_id, topic, status, recipe, created_by, pi_sheet_id)
		values ($1, $2, $3, $4, $5, $6, $7, 'sent', $8, $9, $10)`, p.TenantID, id, in.OrderID, in.ResourceID, in.Destination, in.EdgeID, in.Topic, doc, createdBy, piSheetID); err != nil {
		return ControlRecipe{}, err
	}
	out, err := scanRecipe(tx.QueryRow(ctx, `select `+recipeColumns+` from control_recipe r where r.id = $1`, id))
	if err != nil {
		return out, err
	}
	if err := recordRecipeChange(ctx, tx, p.TenantID, out, nil, createdBy, nil, false); err != nil {
		return out, err
	}
	return out, remember(ctx, tx, idem, http.StatusCreated, out)
}

// ListControlRecipes is the control recipe monitor: newest first, at most f.Limit rows.
func (s *Postgres) ListControlRecipes(ctx context.Context, f RecipeFilter) ([]ControlRecipe, error) {
	out := []ControlRecipe{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+recipeColumns+` from control_recipe r
			where ($1 = '' or r.status = $1) and ($2 = '' or r.destination = $2) and ($3 = '' or r.order_id::text = $3)
			  and ($4 = '' or r.resource_id::text = $4) and ($5 = '' or r.edge_id::text = $5)
			order by r.created_at desc, r.id limit $6`, f.Status, f.Destination, strings.ToLower(f.OrderID), strings.ToLower(f.ResourceID), strings.ToLower(f.EdgeID), f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanRecipe(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// GetControlRecipe returns a recipe with its history and write audit.
func (s *Postgres) GetControlRecipe(ctx context.Context, id string) (ControlRecipeDetail, error) {
	var out ControlRecipeDetail
	out.History, out.Writes = []RecipeEvent{}, []WriteAudit{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := scanRecipe(tx.QueryRow(ctx, `select `+recipeColumns+` from control_recipe r where r.id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return recipeNotFound()
		}
		if err != nil {
			return err
		}
		out.ControlRecipe = c
		rows, err := tx.Query(ctx, `select id::text, from_status, to_status, actor, note, forced, at from control_recipe_event where recipe_id = $1 order by at, id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e RecipeEvent
			if err := rows.Scan(&e.ID, &e.FromStatus, &e.ToStatus, &e.Actor, &e.Note, &e.Forced, &e.At); err != nil {
				rows.Close()
				return err
			}
			out.History = append(out.History, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		wr, err := tx.Query(ctx, `select id::text, phase_path, node, value::text, readback::text, written, deviation, reason, at from edge_write_audit where recipe_id = $1 order by at, id`, id)
		if err != nil {
			return err
		}
		defer wr.Close()
		for wr.Next() {
			var w WriteAudit
			var value string
			var readback *string
			if err := wr.Scan(&w.ID, &w.PhasePath, &w.Node, &value, &readback, &w.Written, &w.Deviation, &w.Reason, &w.At); err != nil {
				return err
			}
			if w.Value, err = decimal.NewFromString(value); err != nil {
				return err
			}
			if readback != nil {
				d, err := decimal.NewFromString(*readback)
				if err != nil {
					return err
				}
				w.Readback = &d
			}
			out.Writes = append(out.Writes, w)
		}
		return wr.Err()
	})
	return out, err
}

// applyRecipeStatus moves a locked recipe to status to (BR-25.11 order), recording history and the event.
// Reporting the status it already has changes nothing, so a connector that lost an answer can repeat it.
func applyRecipeStatus(ctx context.Context, tx pgx.Tx, tenantID string, c ControlRecipe, to string, errText *string, actor string) (ControlRecipe, error) {
	if c.Status == to {
		return c, nil
	}
	if err := domain.RecipeTransition(c.Status, to); err != nil {
		return c, httpx.NewProblem(http.StatusConflict, CodeRecipeTransition, err.Error())
	}
	from := c.Status
	var lastErr *string
	if to == domain.RecipeError {
		lastErr = errText
	}
	if _, err := tx.Exec(ctx, `update control_recipe set status = $3, last_error = $4, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
		tenantID, c.ID, to, lastErr); err != nil {
		return c, err
	}
	c, err := scanRecipe(tx.QueryRow(ctx, `select `+recipeColumns+` from control_recipe r where r.id = $1`, c.ID))
	if err != nil {
		return c, err
	}
	return c, recordRecipeChange(ctx, tx, tenantID, c, &from, actor, errText, false)
}

func lockRecipe(ctx context.Context, tx pgx.Tx, tenantID, id string) (ControlRecipe, error) {
	c, err := scanRecipe(tx.QueryRow(ctx, `select `+recipeColumns+` from control_recipe r where r.tenant_id = $1 and r.id = $2 for update`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, recipeNotFound()
	}
	return c, err
}

// ReportControlRecipe is a kiosk operator's status report on a kiosk recipe: received, in-execution,
// completed or error. A recipe for a process control system is reported by its connector, not here.
func (s *Postgres) ReportControlRecipe(ctx context.Context, id, to string, errText *string, actor string) (ControlRecipe, error) {
	p, _ := tenant.FromContext(ctx)
	var out ControlRecipe
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := lockRecipe(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if c.Destination != domain.DestKiosk {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecipeDestination, "this control recipe goes to a process control system; its connector reports its status")
		}
		out, err = applyRecipeStatus(ctx, tx, p.TenantID, c, to, errText, actor)
		return err
	})
	return out, err
}

// ResendControlRecipe sends a recipe in error again (BR-25.11): at most five times, and with force and a
// reason once a process message for the order was received after the recipe was last sent (a message from
// before the send says nothing about what the process control system did with it). The status goes back to sent.
func (s *Postgres) ResendControlRecipe(ctx context.Context, id string, force bool, reason, actor string, idem Idem) (ControlRecipe, error) {
	p, _ := tenant.FromContext(ctx)
	var out ControlRecipe
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := lockRecipe(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		var messages bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from process_message where tenant_id = $1 and order_id = $2 and deleted_at is null and received_at > $3)`, p.TenantID, c.OrderID, c.LastSentAt).Scan(&messages); err != nil {
			return err
		}
		kind, err := domain.ResendCheck(c.Status, c.SendCount, messages, force, reason)
		switch kind {
		case "transition":
			return httpx.NewProblem(http.StatusConflict, CodeRecipeTransition, err.Error())
		case "limit":
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeResendLimit, err.Error())
		case "force":
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeResendForce, err.Error())
		}
		from := c.Status
		if _, err := tx.Exec(ctx, `update control_recipe set status = 'sent', send_count = send_count + 1, last_sent_at = now(), last_error = null,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if out, err = scanRecipe(tx.QueryRow(ctx, `select `+recipeColumns+` from control_recipe r where r.id = $1`, id)); err != nil {
			return err
		}
		var note *string
		if r := strings.TrimSpace(reason); r != "" {
			note = &r
		}
		if err := recordRecipeChange(ctx, tx, p.TenantID, out, &from, actor, note, force && messages); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}

// EdgeControlRecipes lists the active recipes (sent, received, in-execution) of the node the caller is
// the bound connector of, oldest first.
func (s *Postgres) EdgeControlRecipes(ctx context.Context, edgeID, subject string) ([]ControlRecipe, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ControlRecipe{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := edgeConnector(ctx, tx, p.TenantID, edgeID, subject, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+recipeColumns+` from control_recipe r
			where r.tenant_id = $1 and r.edge_id = $2 and r.status in ('sent', 'received', 'in-execution')
			order by r.created_at, r.id limit 100`, p.TenantID, edgeID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanRecipe(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// EdgeReportControlRecipe records what the connector did with a recipe. The status, when given, moves
// the recipe (received, in-execution, completed, error); a write made (written true) is accepted only while
// the recipe is in-execution (BR-25.15), and every write must be one of the recipe's setpoints at its
// value. Each write is stored in edge_write_audit; a write whose readback is missing or differs by more
// than ReadbackTolerance is a deviation and publishes controlrecipe.deviation.
func (s *Postgres) EdgeReportControlRecipe(ctx context.Context, edgeID, subject, id string, rep EdgeRecipeReport) (ControlRecipe, error) {
	p, _ := tenant.FromContext(ctx)
	// A deviation also becomes a process message of category deviation (the tenant's definition of it, else the default).
	devCat, err := s.ResolveProcessCategory(ctx, "deviation")
	if err != nil {
		devCat, _ = domain.CategoryByCode("deviation")
	}
	var out ControlRecipe
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := edgeConnector(ctx, tx, p.TenantID, edgeID, subject, true); err != nil {
			return err
		}
		c, err := lockRecipe(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if c.EdgeID == nil || !strings.EqualFold(*c.EdgeID, edgeID) {
			return recipeNotFound()
		}
		if rep.Status != "" {
			if c, err = applyRecipeStatus(ctx, tx, p.TenantID, c, rep.Status, rep.Error, subject); err != nil {
				return err
			}
		}
		if len(rep.Writes) > 0 {
			var doc domain.RecipeDoc
			if err := json.Unmarshal(c.Recipe, &doc); err != nil {
				return err
			}
			if err := auditEdgeWrites(ctx, tx, p.TenantID, c, doc, rep.Writes, devCat); err != nil {
				return err
			}
		}
		out = c
		return nil
	})
	return out, err
}

func auditEdgeWrites(ctx context.Context, tx pgx.Tx, tenantID string, c ControlRecipe, doc domain.RecipeDoc, writes []EdgeWrite, devCat domain.Category) error {
	setpoints := doc.Setpoints()
	for _, w := range writes {
		var match bool
		for _, sp := range setpoints {
			if sp.Node == w.Node && sp.PhasePath == w.PhasePath && decimal.NewFromFloat(sp.Value).Equal(w.Value) {
				match = true
				break
			}
		}
		if !match {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeRecipeDestination, fmt.Sprintf("the write to %s in %s is not a setpoint of this control recipe", w.Node, w.PhasePath))
		}
		if w.Written && c.Status != domain.RecipeInExecution {
			return httpx.NewProblem(http.StatusConflict, CodeRecipeTransition, "a setpoint is written only while the control recipe is in-execution")
		}
		deviation := false
		if w.Written {
			switch {
			case w.Readback != nil:
				deviation = w.Value.Sub(*w.Readback).Abs().GreaterThan(domain.ReadbackTolerance(w.NodeType, w.Value))
			case c.Destination != domain.DestPCSMQTT:
				// MQTT has no readback, so a missing one proves nothing; every other protocol must give one.
				deviation = true
			}
		}
		at := w.At
		if at.IsZero() {
			at = time.Now().UTC()
		}
		if _, err := tx.Exec(ctx, `insert into edge_write_audit (tenant_id, id, recipe_id, edge_id, phase_path, node, value, readback, written, deviation, reason, at, node_type)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`, tenantID, ids.New(), c.ID, *c.EdgeID, w.PhasePath, w.Node, w.Value.String(), nullDecimal(w.Readback), w.Written, deviation, w.Reason, at, w.NodeType); err != nil {
			return err
		}
		if deviation {
			var rb *string
			if w.Readback != nil {
				v := w.Readback.String()
				rb = &v
			}
			if err := enqueue(ctx, tx, recipeSource, c.ID, sfevents.ControlRecipeDeviation, tenantID, sfevents.ControlRecipeDeviationData{
				RecipeID: c.ID, OrderID: c.OrderID, EdgeID: *c.EdgeID, PhasePath: w.PhasePath, Node: w.Node, Written: w.Value.String(), Readback: rb}); err != nil {
				return err
			}
			if err := recordDeviationMessage(ctx, tx, tenantID, c, w, at, devCat); err != nil {
				return err
			}
		}
	}
	return nil
}

func nullDecimal(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

// endRecipesFromMessage ends the in-execution recipes of the message's order that run on a process control
// system when the message says the batch ended (domain.RecipeEnd): completed, or error with the reason. The
// recipe's history names the message as the actor, so the monitor shows what ended it. Called with the
// message's transaction, so the message and the status change commit together.
func endRecipesFromMessage(ctx context.Context, tx pgx.Tx, tenantID, messageID string, in domain.NewMessage, values []domain.Characteristic) error {
	if in.OrderID == "" || (in.CategoryCode != "order-status" && in.CategoryCode != "phase-end") {
		return nil
	}
	rows, err := tx.Query(ctx, `select `+recipeColumns+` from control_recipe r
		where r.tenant_id = $1 and r.order_id = $2 and r.status = 'in-execution' and r.destination <> 'kiosk' order by r.created_at for update`, tenantID, in.OrderID)
	if err != nil {
		return err
	}
	var recipes []ControlRecipe
	for rows.Next() {
		c, err := scanRecipe(rows)
		if err != nil {
			rows.Close()
			return err
		}
		recipes = append(recipes, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range recipes {
		var doc domain.RecipeDoc
		if err := json.Unmarshal(c.Recipe, &doc); err != nil {
			return err
		}
		to, reason, ends := domain.RecipeEnd(in.CategoryCode, in.PhasePath, values, doc)
		if !ends {
			continue
		}
		var errText *string
		if reason != "" {
			errText = &reason
		}
		if _, err := applyRecipeStatus(ctx, tx, tenantID, c, to, errText, "process-message:"+messageID); err != nil {
			return err
		}
	}
	return nil
}

// recordDeviationMessage turns a setpoint deviation into a process message of category deviation, so it
// reaches the batch record, quality and the other destinations of that category like any deviation (BR-25.13).
// The message is idempotent by the recipe, the setpoint and the time of the write, so a report the connector
// repeats does not make a second one.
func recordDeviationMessage(ctx context.Context, tx pgx.Tx, tenantID string, c ControlRecipe, w EdgeWrite, at time.Time, cat domain.Category) error {
	if c.OrderID == "" {
		return nil
	}
	values := []domain.Characteristic{
		{Name: "setpoint", Type: "numeric", Value: w.Value.String()},
		{Name: "node", Type: "text", Value: w.Node},
	}
	if w.Readback != nil {
		values = append(values, domain.Characteristic{Name: "readback", Type: "numeric", Value: w.Readback.String()})
	} else {
		values = append(values, domain.Characteristic{Name: "readback", Type: "text", Value: "none"})
	}
	in := domain.NewMessage{Source: domain.SourceEdge, SourceMessageID: fmt.Sprintf("recipe-deviation:%s:%s:%s:%d", c.ID, w.PhasePath, w.Node, at.UnixNano()),
		CategoryCode: "deviation", OrderID: c.OrderID, PhasePath: w.PhasePath, OccurredAt: at, Values: values}
	now := time.Now().UTC()
	raw, deliveries, status, vals, err := prepareProcessMessage(in, cat, now)
	if err != nil {
		return err
	}
	_, err = ingestProcessMessageTx(ctx, tx, tenantID, in, cat, now, raw, deliveries, status, vals)
	return err
}
