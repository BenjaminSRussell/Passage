package generated

import "context"

type LegalEntityRepository interface {
	List(context.Context, int) ([]LegalEntity, error)
	Get(context.Context, string) (LegalEntity, error)
	Create(context.Context, LegalEntity) (LegalEntity, error)
	Update(context.Context, string, map[string]any, int) (LegalEntity, error)
	Delete(context.Context, string, int) error
}
