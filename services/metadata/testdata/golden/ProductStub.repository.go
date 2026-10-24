package generated

import "context"

type ProductStubRepository interface {
	List(context.Context, int) ([]ProductStub, error)
	Get(context.Context, string) (ProductStub, error)
	Create(context.Context, ProductStub) (ProductStub, error)
	Update(context.Context, string, map[string]any, int) (ProductStub, error)
	Delete(context.Context, string, int) error
}
