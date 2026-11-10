package events

const (
	ProductCreated      = "dev.passage.product.product.created.v1"
	ProductUpdated      = "dev.passage.product.product.updated.v1"
	VariantCreated      = "dev.passage.product.variant.created.v1"
	ProductReleased     = "dev.passage.product.product.released.v1"
	ReleasedUpdated     = "dev.passage.product.releasedproduct.updated.v1"
	IdentifierAdded     = "dev.passage.product.identifier.added.v1"
	ConfigurationSolved = "dev.passage.product.configuration.solved.v1"
	RelationChanged     = "dev.passage.product.relation.changed.v1"
)

type ProductCreatedData struct {
	ProductID      string `json:"productId"`
	ProductNumber  string `json:"productNumber"`
	Name           string `json:"name"`
	Subtype        string `json:"subtype"`
	ProductType    string `json:"productType"`
	BaseUOM        string `json:"baseUom"`
	LifecycleState string `json:"lifecycleState"`
	Version        int    `json:"version"`
}

type ProductUpdatedData struct {
	ProductID      string   `json:"productId"`
	ProductNumber  string   `json:"productNumber"`
	Name           string   `json:"name"`
	ProductType    string   `json:"productType"`
	LifecycleState string   `json:"lifecycleState"`
	ChangedFields  []string `json:"changedFields"`
	Version        int      `json:"version"`
}

type VariantCreatedData struct {
	MasterID        string              `json:"masterId"`
	VariantIDs      []string            `json:"variantIds"`
	DimensionValues []map[string]string `json:"dimensionValues"`
}

type ProductReleasedData struct {
	ProductID          string `json:"productId"`
	ReleasedProductID  string `json:"releasedProductId"`
	LegalEntityID      string `json:"legalEntityId"`
	ItemNumber         string `json:"itemNumber"`
	ItemGroupID        string `json:"itemGroupId"`
	ItemModelGroupID   string `json:"itemModelGroupId"`
	DefaultSiteID      string `json:"defaultSiteId"`
	DefaultWarehouseID string `json:"defaultWarehouseId"`
}

type ReleasedUpdatedData struct {
	ReleasedProductID string          `json:"releasedProductId"`
	ChangedFields     []string        `json:"changedFields"`
	LifecycleState    string          `json:"lifecycleState"`
	Blocks            map[string]bool `json:"blocks"`
}

type IdentifierAddedData struct {
	ProductID string `json:"productId"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	UOM       string `json:"uomId"`
}

type ConfigurationSolvedData struct {
	ModelID         string            `json:"modelId"`
	MasterID        string            `json:"masterId"`
	Selections      map[string]string `json:"selections"`
	ResultProductID string            `json:"resultProductId"`
}

type RelationChangedData struct {
	FromProductID string `json:"fromProductId"`
	ToProductID   string `json:"toProductId"`
	TypeCode      string `json:"typeCode"`
	Action        string `json:"action"`
}
