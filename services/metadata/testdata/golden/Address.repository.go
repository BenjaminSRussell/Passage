package generated

import "context"

type AddressRepository interface {
	List(context.Context, int) ([]Address, error)
	Get(context.Context, string) (Address, error)
	Create(context.Context, Address) (Address, error)
	Update(context.Context, string, map[string]any, int) (Address, error)
	Delete(context.Context, string, int) error
}
