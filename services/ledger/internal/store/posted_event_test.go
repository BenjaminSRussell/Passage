package store

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// Every journal.posted event carries the ledger's legal entity in the payload and in the CloudEvent
// legalentityid extension (wave 15's reporting consumer refuses events without it): direct posting,
// reversal and batch posting all go through writeEntry.
func TestPostgres_JournalPostedCarriesLegalEntity(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	posted, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "LE-1", "10.00"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reverseWith(t, e, posted.Entries[0].ID, "LE-REV", ""); err != nil {
		t.Fatal(err)
	}
	b, err := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "5.00"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err = e.store.SubmitBatch(e.ctx, b.ID, 0, testToday); err != nil {
		t.Fatal(err)
	}
	if _, err := postBatch(e, e.ctx, b, "LE-BATCH", true); err != nil {
		t.Fatal(err)
	}

	var payloads [][]byte
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(e.ctx, `select payload from outbox where type = $1`, ledgerevents.JournalPosted)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			payloads = append(payloads, raw)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 3 {
		t.Fatalf("journal.posted events = %d, want direct, reversal and batch", len(payloads))
	}
	for _, raw := range payloads {
		var evt struct {
			LegalEntityID string `json:"legalentityid"`
			Data          struct {
				LegalEntityID string `json:"legalEntityId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		if evt.LegalEntityID != e.le || evt.Data.LegalEntityID != e.le {
			t.Fatalf("legalentityid extension %q, data.legalEntityId %q, want %q", evt.LegalEntityID, evt.Data.LegalEntityID, e.le)
		}
	}
}
