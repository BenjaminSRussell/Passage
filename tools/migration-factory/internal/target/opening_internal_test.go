package target

import (
	"errors"
	"net/http"
	"testing"
)

// The inventory ledger names a row whose reference another batch already opened (INV-OPB-005); the factory takes it as a refused
// row like an invalid one, so the row is rejected and the rest of the chunk is sent again.
func TestOpeningAnswer_ACrossBatchDuplicateIsARefusedRow(t *testing.T) {
	body := []byte(`{"code":"INV-OPB-005","detail":"already opened","errors":{"items[2].reference":"was already opened by batch mf-onhand-1"}}`)
	_, err := openingAnswer(http.StatusUnprocessableEntity, body, "k", 3, CodeInventoryRowsInvalid, CodeInventoryRowsHeld)
	var rr *RowsRefused
	if !errors.As(err, &rr) || rr.Code != "INV-OPB-005" || len(rr.Rows) != 1 || rr.Rows[2] == "" {
		t.Fatalf("err = %v", err)
	}
	// Another 422 code is not about rows.
	if _, err := openingAnswer(http.StatusUnprocessableEntity, []byte(`{"code":"INV-OPB-002","errors":{"items[1].reference":"x"}}`), "k", 3, CodeInventoryRowsInvalid, CodeInventoryRowsHeld); errors.As(err, &rr) {
		t.Fatalf("a key conflict was taken as refused rows: %v", err)
	}
}
