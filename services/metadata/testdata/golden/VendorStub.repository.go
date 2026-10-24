package generated

import "context"

type VendorStubRepository interface {
	List(context.Context, int) ([]VendorStub, error)
	Get(context.Context, string) (VendorStub, error)
	Create(context.Context, VendorStub) (VendorStub, error)
	Update(context.Context, string, map[string]any, int) (VendorStub, error)
	Delete(context.Context, string, int) error
}
