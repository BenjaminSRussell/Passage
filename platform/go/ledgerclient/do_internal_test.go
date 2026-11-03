package ledgerclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestDo_RefusesARequestThatNamesNoDuty(t *testing.T) {
	_, err := (&Client{}).do(context.Background(), "ledger", "l", http.MethodGet, "/x", nil, nil, "", "le")
	if !errors.Is(err, ErrNoDuty) {
		t.Fatalf("got %v", err)
	}
}
