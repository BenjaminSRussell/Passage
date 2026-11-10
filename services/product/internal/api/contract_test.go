package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The product response is camelCase end to end, the same casing as the request bodies and the OpenAPI schema (#558).
func TestProductResponseKeysAreCamelCase(t *testing.T) {
	f := newFake()
	r := newRouter(f)
	created := do(r, http.MethodPost, "/v1/product/products", `{"productNumber":"CC1","name":"Camel","baseUom":"KG"}`, "product.write")
	if created.Code != http.StatusCreated {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(created.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "productNumber", "subtype", "masterId", "productType", "name", "description", "searchName", "baseUom",
		"dimensionGroupId", "storageDimensionGroupId", "trackingDimensionGroupId", "itemGroupId", "batchTracked", "serialTracked",
		"catchWeight", "lifecycleState", "dimensionValues", "variantHash", "service", "version"}
	if len(got) != len(want) {
		t.Fatalf("product has %d keys, want %d: %s", len(got), len(want), created.Body.String())
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("product response lacks key %q: %s", k, created.Body.String())
		}
	}
}
