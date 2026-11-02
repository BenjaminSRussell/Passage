package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func cloneCapture(c store.Capture) store.Capture {
	c.Reasons = slices.Clone(c.Reasons)
	c.Invoice.Lines = slices.Clone(c.Invoice.Lines)
	return c
}

func (s *Store) CreateCapture(ctx context.Context, in store.Capture) (out store.Capture, created bool, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		for _, c := range t.captures {
			if c.LegalEntityID == in.LegalEntityID && c.DocumentSHA256 == in.DocumentSHA256 {
				out = cloneCapture(c)
				return nil
			}
		}
		in.CreatedAt = time.Now().UTC()
		t.captures[in.ID] = cloneCapture(in)
		out, created = cloneCapture(in), true
		return nil
	})
	return out, created, err
}

func (s *Store) GetCapture(ctx context.Context, id string) (out store.Capture, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		canonical, valid := ids.Canonical(id)
		c, ok := t.captures[canonical]
		if !valid || !ok {
			return store.ErrCaptureNotFound
		}
		out = cloneCapture(c)
		return nil
	})
	return out, err
}

func (s *Store) FindCapture(ctx context.Context, legalEntityID, digest string) (out store.Capture, found bool, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		for _, c := range t.captures {
			if c.LegalEntityID == legalEntityID && c.DocumentSHA256 == digest {
				out, found = cloneCapture(c), true
			}
		}
		return nil
	})
	return out, found, err
}
