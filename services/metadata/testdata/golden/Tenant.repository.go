package generated

import "context"

type TenantRepository interface {
	List(context.Context, int) ([]Tenant, error)
	Get(context.Context, string) (Tenant, error)
	Create(context.Context, Tenant) (Tenant, error)
	Update(context.Context, string, map[string]any, int) (Tenant, error)
	Delete(context.Context, string, int) error
}
