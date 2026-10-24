package generated

import "context"

type RoleRepository interface {
	List(context.Context, int) ([]Role, error)
	Get(context.Context, string) (Role, error)
	Create(context.Context, Role) (Role, error)
	Update(context.Context, string, map[string]any, int) (Role, error)
	Delete(context.Context, string, int) error
}
