package domain

import "github.com/shopspring/decimal"

const (
	SubtypeProduct = "product"
	SubtypeMaster  = "master"
	SubtypeVariant = "variant"

	TypeItem    = "item"
	TypeService = "service"

	StateDraft    = "draft"
	StateActive   = "active"
	StatePhaseOut = "phase_out"
	StateObsolete = "obsolete"

	MaxVariantsDefault = 10000
	ExplodeCheckpoint  = 500
	MaxProductNumber   = 40
	MaxRelations       = 200
	MaxAttributes      = 200

	DefaultVariantPattern = "{master}-{size}-{color}-{style}-{config}"
)

// ProductNumberPattern is FR-8.1.
const ProductNumberPattern = `^[A-Z0-9][A-Z0-9._-]{0,39}$`

// Product is the shared tenant-level item, master or variant.
// ProductName is what a customer-facing screen shows for a product: its number and name, nothing else.
type ProductName struct {
	ID            string `json:"id"`
	ProductNumber string `json:"productNumber"`
	Name          string `json:"name"`
}

type Product struct {
	ID                       string             `json:"id"`
	ProductNumber            string             `json:"productNumber"`
	Subtype                  string             `json:"subtype"`
	MasterID                 string             `json:"masterId"`
	ProductType              string             `json:"productType"`
	Name                     string             `json:"name"`
	Description              string             `json:"description"`
	SearchName               string             `json:"searchName"`
	BaseUOM                  string             `json:"baseUom"`
	DimensionGroupID         string             `json:"dimensionGroupId"`
	StorageDimensionGroupID  string             `json:"storageDimensionGroupId"`
	TrackingDimensionGroupID string             `json:"trackingDimensionGroupId"`
	ItemGroupID              string             `json:"itemGroupId"`
	BatchTracked             bool               `json:"batchTracked"`
	SerialTracked            bool               `json:"serialTracked"`
	CatchWeight              bool               `json:"catchWeight"`
	LifecycleState           string             `json:"lifecycleState"`
	DimensionValues          map[string]string  `json:"dimensionValues"`
	VariantHash              string             `json:"variantHash"`
	Service                  *ServiceAttributes `json:"service"`
	Version                  int                `json:"version"`
}

// ProductPatch is a partial update of a product; nil leaves a field alone.
type ProductPatch struct {
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	SearchName     *string `json:"searchName"`
	LifecycleState *string `json:"lifecycleState"`
	ItemGroupID    *string `json:"itemGroupId"`
	BatchTracked   *bool   `json:"batchTracked"`
	SerialTracked  *bool   `json:"serialTracked"`
	CatchWeight    *bool   `json:"catchWeight"`
}

// Dimension describes one product dimension and its allowed values.
type Dimension struct {
	Code   string   `json:"code"`
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// DimensionGroup is a named set of product, storage or tracking dimensions.
type DimensionGroup struct {
	ID     string      `json:"id"`
	Code   string      `json:"code"`
	Name   string      `json:"name"`
	Kind   string      `json:"kind"` // product, storage, tracking
	Fields []Dimension `json:"fields"`
}

// Variant is one exploded combination of a master.
type Variant struct {
	ProductNumber   string            `json:"productNumber"`
	DimensionValues map[string]string `json:"dimensionValues"`
	VariantHash     string            `json:"variantHash"`
}

// ExplosionRequest is the input to variant generation.
type ExplosionRequest struct {
	MasterNumber string              `json:"masterNumber"`
	Dimensions   []Dimension         `json:"dimensions"`
	Selected     map[string][]string `json:"selected"`
	Explicit     []map[string]string `json:"explicit"`
	MaxVariants  int                 `json:"maxVariants"`
	Pattern      string              `json:"pattern"`
}

// ExplosionResult reports created and already-existing combinations.
type ExplosionResult struct {
	Created   []Variant `json:"created"`
	Skipped   []Variant `json:"skipped"`
	Remaining int       `json:"remaining"`
}

// ItemModelGroup carries costing and inventory posting flags.
type ItemModelGroup struct {
	ID                 string `json:"id"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	CostingMethod      string `json:"costingMethod"`
	Stocked            bool   `json:"stocked"`
	PhysicalNegative   bool   `json:"physicalNegative"`
	FinancialNegative  bool   `json:"financialNegative"`
	PostPhysical       bool   `json:"postPhysical"`
	PostFinancial      bool   `json:"postFinancial"`
	Registration       bool   `json:"registration"`
	Receiving          bool   `json:"receiving"`
	Picking            bool   `json:"picking"`
	Deduction          bool   `json:"deduction"`
	Quarantine         bool   `json:"quarantine"`
	IncludePhysicalVal bool   `json:"includePhysicalVal"`
}

// ItemGroup is a posting hook grouping.
type ItemGroup struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// SiteDefault is a per-site warehouse and order-setting override.
type SiteDefault struct {
	SiteID       string          `json:"siteId"`
	WarehouseID  string          `json:"warehouseId"`
	MinQty       decimal.Decimal `json:"minQty"`
	MultipleQty  decimal.Decimal `json:"multipleQty"`
	StdQty       decimal.Decimal `json:"stdQty"`
	LeadTimeDays int             `json:"leadTimeDays"`
}

// ReleaseDefaults are entity-level defaults plus optional site overrides.
type ReleaseDefaults struct {
	LegalEntityID      string                 `json:"legalEntityId"`
	ItemGroupID        string                 `json:"itemGroupId"`
	ItemModelGroupID   string                 `json:"itemModelGroupId"`
	InventoryUOM       string                 `json:"inventoryUom"`
	PurchaseUOM        string                 `json:"purchaseUom"`
	SalesUOM           string                 `json:"salesUom"`
	BOMUOM             string                 `json:"bOMUom"`
	DefaultSiteID      string                 `json:"defaultSiteId"`
	DefaultWarehouseID string                 `json:"defaultWarehouseId"`
	SiteDefaults       map[string]SiteDefault `json:"siteDefaults"`
}

// ReleasedProduct is a product usable in one legal entity.
type ReleasedProduct struct {
	ID                 string                 `json:"id"`
	ProductID          string                 `json:"productId"`
	LegalEntityID      string                 `json:"legalEntityId"`
	ItemNumber         string                 `json:"itemNumber"`
	ItemGroupID        string                 `json:"itemGroupId"`
	ItemModelGroupID   string                 `json:"itemModelGroupId"`
	DefaultSiteID      string                 `json:"defaultSiteId"`
	DefaultWarehouseID string                 `json:"defaultWarehouseId"`
	LifecycleState     string                 `json:"lifecycleState"`
	BlockedPurchase    bool                   `json:"blockedPurchase"`
	BlockedSales       bool                   `json:"blockedSales"`
	BlockedInventory   bool                   `json:"blockedInventory"`
	BlockedProduction  bool                   `json:"blockedProduction"`
	SiteDefaults       map[string]SiteDefault `json:"siteDefaults"`
	HasFinancialTxn    bool                   `json:"hasFinancialTxn"`
	Version            int                    `json:"version"`
}

// ServiceAttributes extend a product of type service (FR-8.17).
type ServiceAttributes struct {
	CategoryCode         string `json:"categoryCode"`
	ServiceUOM           string `json:"serviceUom"`
	PurchasingText       string `json:"purchasingText"`
	RequiresServiceEntry bool   `json:"requiresServiceEntry"`
	ValuationClass       string `json:"valuationClass"`
}

// RelationType describes a seeded or tenant relation kind.
type RelationType struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Symmetric bool   `json:"symmetric"`
}

// Relation links two products.
type Relation struct {
	TypeCode      string `json:"typeCode"`
	FromProductID string `json:"fromProductId"`
	ToProductID   string `json:"toProductId"`
	SortOrder     int    `json:"sortOrder"`
	ValidFrom     string `json:"validFrom"`
	ValidTo       string `json:"validTo"`
}

// LifecycleState is a tenant-defined state mapped to a FR-8.10 category.
type LifecycleState struct {
	Code          string            `json:"code"`
	Name          string            `json:"name"`
	Category      string            `json:"category"`
	DefaultForNew bool              `json:"defaultForNew"`
	AppliesTo     []string          `json:"appliesTo"`
	Policies      map[string]string `json:"policies"` // process -> allow|warn|block
}

// ProcessDecision is the result of a process-policy query.
type ProcessDecision struct {
	Policy string `json:"policy"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// AttributeType is a typed attribute definition.
type AttributeType struct {
	Code     string           `json:"code"`
	Kind     string           `json:"kind"`
	Unit     string           `json:"unit"`
	Min      *decimal.Decimal `json:"min"`
	Max      *decimal.Decimal `json:"max"`
	Enum     []string         `json:"enum"`
	Required bool             `json:"required"`
}

// Conversion is a product- or class-level unit conversion.
type Conversion struct {
	From          string          `json:"from"`
	To            string          `json:"to"`
	Class         string          `json:"class"`
	Factor        decimal.Decimal `json:"factor"`
	Offset        decimal.Decimal `json:"offset"`
	ProductScoped bool            `json:"productScoped"`
}

// Identifier is a GS1 or custom product code.
type Identifier struct {
	ProductID string `json:"productId"`
	UOM       string `json:"uom"`
	Type      string `json:"type"`
	Value     string `json:"value"`
	Primary   bool   `json:"primary"`
}

// DocumentInfoRecord is a DIR (FR-8.20).
type DocumentInfoRecord struct {
	TypeCode    string             `json:"typeCode"`
	DocNumber   string             `json:"docNumber"`
	Part        string             `json:"part"`
	DocVersion  string             `json:"docVersion"`
	Description string             `json:"description"`
	Status      string             `json:"status"`
	Links       []DocumentLink     `json:"links"`
	Originals   []DocumentOriginal `json:"originals"`
}

// DocumentLink points a DIR at another object.
type DocumentLink struct {
	TargetType string `json:"targetType"`
	TargetID   string `json:"targetId"`
}

// DocumentOriginal is one file on a DIR version.
type DocumentOriginal struct {
	StorageKey string `json:"storageKey"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"sizeBytes"`
	MIME       string `json:"mime"`
	Text       string `json:"text"`
}

// DocumentTypeSettings control originals and versions.
type DocumentTypeSettings struct {
	Code          string   `json:"code"`
	MaxOriginals  int      `json:"maxOriginals"`
	AllowedMIMEs  []string `json:"allowedMIMEs"`
	VersionScheme string   `json:"versionScheme"`
}

// GroupChangeItem is one released product in a conversion job.
type GroupChangeItem struct {
	ReleasedProductID string          `json:"releasedProductId"`
	OnHand            decimal.Decimal `json:"onHand"`
}

// JobItemResult is the per-item outcome of a mass job.
type JobItemResult struct {
	ID     string `json:"id"`
	Status string `json:"status"` // changed, skipped, obsolete, deleted
	Code   string `json:"code,omitempty"`
}

// Catalog is a named category subtree with a validity window.
type Catalog struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Hierarchy string `json:"hierarchy"`
	Root      string `json:"root"`
	ValidFrom string `json:"validFrom"`
	ValidTo   string `json:"validTo"`
}
