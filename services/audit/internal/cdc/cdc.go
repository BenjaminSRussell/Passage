// Package cdc is the wave-4 seam: audit consumes field changes without
// importing Kafka, Debezium or services/batch.
package cdc

import (
	"context"

	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
)

// Consumer applies one CDC change. The HTTP ingest adapter and a future
// Kafka consumer both call this.
type Consumer interface {
	Apply(ctx context.Context, change domain.Change) error
}
