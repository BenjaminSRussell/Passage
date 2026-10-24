package generated

import "context"

type UserRepository interface {
	List(context.Context, int) ([]User, error)
	Get(context.Context, string) (User, error)
	Create(context.Context, User) (User, error)
	Update(context.Context, string, map[string]any, int) (User, error)
	Delete(context.Context, string, int) error
}
