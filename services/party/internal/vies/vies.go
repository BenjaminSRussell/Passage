package vies

import (
	"context"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

type Unavailable struct{}

func (Unavailable) Check(context.Context, string, string) (domain.Status, error) {
	return domain.StatusUnverified, nil
}
