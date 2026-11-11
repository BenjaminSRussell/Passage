package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// RecipeTarget says where the control recipe of a released batch order of one item goes (#25, 25-5).
type RecipeTarget struct {
	LegalEntityID string    `json:"legalEntityId"`
	ItemID        string    `json:"itemId"`
	ResourceID    string    `json:"resourceId"`
	Destination   string    `json:"destination"` // kiosk, pcs-opcua or pcs-mqtt
	EdgeID        *string   `json:"edgeId,omitempty"`
	Topic         *string   `json:"topic,omitempty"`
	UpdatedBy     string    `json:"updatedBy"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ControlRecipeLink is what a batch order keeps of its control recipe.
type ControlRecipeLink struct {
	ControlRecipeID *string `json:"controlRecipeId"`
	Error           *string `json:"error"`
}

const recipeTargetCols = `legal_entity_id::text, item_id::text, resource_id::text, destination, edge_id::text, topic, updated_by, updated_at`

func scanRecipeTarget(row pgx.Row) (RecipeTarget, error) {
	var t RecipeTarget
	err := row.Scan(&t.LegalEntityID, &t.ItemID, &t.ResourceID, &t.Destination, &t.EdgeID, &t.Topic, &t.UpdatedBy, &t.UpdatedAt)
	return t, err
}

// PutRecipeTarget sets the target of an item in a legal entity, replacing the one it had.
func (s *Postgres) PutRecipeTarget(ctx context.Context, t RecipeTarget) (RecipeTarget, error) {
	p := principal(ctx)
	var out RecipeTarget
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanRecipeTarget(tx.QueryRow(ctx, `insert into recipe_target (tenant_id, legal_entity_id, item_id, resource_id, destination, edge_id, topic, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, legal_entity_id, item_id) do update set resource_id = excluded.resource_id, destination = excluded.destination,
				edge_id = excluded.edge_id, topic = excluded.topic, updated_by = excluded.updated_by, updated_at = now()
			returning `+recipeTargetCols, p.TenantID, t.LegalEntityID, t.ItemID, t.ResourceID, t.Destination, t.EdgeID, t.Topic, p.UserID))
		return err
	})
	return out, err
}

// GetRecipeTarget returns the target of an item, and false when the item has none.
func (s *Postgres) GetRecipeTarget(ctx context.Context, legalEntityID, itemID string) (RecipeTarget, bool, error) {
	var out RecipeTarget
	found := true
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanRecipeTarget(tx.QueryRow(ctx, `select `+recipeTargetCols+` from recipe_target where legal_entity_id = $1 and item_id = $2`, legalEntityID, itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			found = false
			return nil
		}
		return err
	})
	return out, found, err
}

// ListRecipeTargets returns the targets the caller's legal entity scope sees, by item.
func (s *Postgres) ListRecipeTargets(ctx context.Context, limit int) ([]RecipeTarget, error) {
	out := []RecipeTarget{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+recipeTargetCols+` from recipe_target order by legal_entity_id, item_id limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanRecipeTarget(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteRecipeTarget removes the target of an item; it reports whether there was one.
func (s *Postgres) DeleteRecipeTarget(ctx context.Context, legalEntityID, itemID string) (bool, error) {
	deleted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from recipe_target where legal_entity_id = $1 and item_id = $2`, legalEntityID, itemID)
		deleted = err == nil && tag.RowsAffected() > 0
		return err
	})
	return deleted, err
}

// SetControlRecipe records the control recipe generated for an order, or the reason it could not be.
func (s *Postgres) SetControlRecipe(ctx context.Context, orderID string, recipeID, errText *string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update batch_order set control_recipe_id = $2, control_recipe_error = $3 where id = $1`, orderID, recipeID, errText)
		if err == nil && tag.RowsAffected() == 0 {
			return notFound(CodeOrderNotFound, "batch order", orderID)
		}
		return err
	})
}

// GetControlRecipeLink reads what an order keeps of its control recipe.
func (s *Postgres) GetControlRecipeLink(ctx context.Context, orderID string) (ControlRecipeLink, error) {
	var out ControlRecipeLink
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select control_recipe_id::text, control_recipe_error from batch_order where id = $1`, orderID).Scan(&out.ControlRecipeID, &out.Error)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeOrderNotFound, "batch order", orderID)
		}
		return err
	})
	return out, err
}
