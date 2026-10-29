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
type Product struct {
	ID                      string
	ProductNumber           string
	Subtype                 string
	MasterID                string
	ProductType             string
	Name                    string
	Description             string
	SearchName              string
	BaseUOM                 string
	DimensionGroupID        string
	StorageDimensionGroupID string
	TrackingDimensionGroupID string
	LifecycleState          string
	DimensionValues         map[string]string
	VariantHash             string
	Service                 *ServiceAttributes
	Version                 int
}

// Dimension describes one product dimension and its allowed values.
type Dimension struct {
	Code   string
	Name   string
	Values []string
}

// DimensionGroup is a named set of product, storage or tracking dimensions.
type DimensionGroup struct {
	ID     string
	Code   string
	Name   string
	Kind   string // product, storage, tracking
	Fields []Dimension
}

// Variant is one exploded combination of a master.
type Variant struct {
	ProductNumber   string
	DimensionValues map[string]string
	VariantHash     string
}

// ExplosionRequest is the input to variant generation.
type ExplosionRequest struct {
	MasterNumber string
	Dimensions   []Dimension
	Selected     map[string][]string
	Explicit     []map[string]string
	MaxVariants  int
	Pattern      string
}

// ExplosionResult reports created and already-existing combinations.
type ExplosionResult struct {
	Created   []Variant
	Skipped   []Variant
	Remaining int
}

// ItemModelGroup carries costing and inventory posting flags.
type ItemModelGroup struct {
	ID                 string
	Code               string
	Name               string
	CostingMethod      string
	Stocked            bool
	PhysicalNegative   bool
	FinancialNegative  bool
	PostPhysical       bool
	PostFinancial      bool
	Registration       bool
	Receiving          bool
	Picking            bool
	Deduction          bool
	Quarantine         bool
	IncludePhysicalVal bool
}

// ItemGroup is a posting hook grouping.
type ItemGroup struct {
	ID   string
	Code string
	Name string
}

// SiteDefault is a per-site warehouse and order-setting override.
type SiteDefault struct {
	SiteID       string
	WarehouseID  string
	MinQty       decimal.Decimal
	MultipleQty  decimal.Decimal
	StdQty       decimal.Decimal
	LeadTimeDays int
}

// ReleaseDefaults are entity-level defaults plus optional site overrides.
type ReleaseDefaults struct {
	LegalEntityID     string
	ItemGroupID       string
	ItemModelGroupID  string
	InventoryUOM      string
	PurchaseUOM       string
	SalesUOM          string
	BOMUOM            string
	DefaultSiteID     string
	DefaultWarehouseID string
	SiteDefaults      map[string]SiteDefault
}

// ReleasedProduct is a product usable in one legal entity.
type ReleasedProduct struct {
	ID                   string
	ProductID            string
	LegalEntityID        string
	ItemNumber           string
	ItemGroupID          string
	ItemModelGroupID     string
	DefaultSiteID        string
	DefaultWarehouseID   string
	LifecycleState       string
	BlockedPurchase      bool
	BlockedSales         bool
	BlockedInventory     bool
	BlockedProduction    bool
	SiteDefaults         map[string]SiteDefault
	HasFinancialTxn      bool
	Version              int
}

// ServiceAttributes extend a product of type service (FR-8.17).
type ServiceAttributes struct {
	CategoryCode         string
	ServiceUOM           string
	PurchasingText       string
	RequiresServiceEntry bool
	ValuationClass       string
}

// RelationType describes a seeded or tenant relation kind.
type RelationType struct {
	Code      string
	Name      string
	Symmetric bool
}

// Relation links two products.
type Relation struct {
	TypeCode      string
	FromProductID string
	ToProductID   string
	SortOrder     int
	ValidFrom     string
	ValidTo       string
}

// LifecycleState is a tenant-defined state mapped to a FR-8.10 category.
type LifecycleState struct {
	Code          string
	Name          string
	Category      string
	DefaultForNew bool
	AppliesTo     []string
	Policies      map[string]string // process -> allow|warn|block
}

// ProcessDecision is the result of a process-policy query.
type ProcessDecision struct {
	Policy string
	Code   string
	Detail string
}

// AttributeType is a typed attribute definition.
type AttributeType struct {
	Code     string
	Kind     string
	Unit     string
	Min      *decimal.Decimal
	Max      *decimal.Decimal
	Enum     []string
	Required bool
}

// Conversion is a product- or class-level unit conversion.
type Conversion struct {
	From, To, Class string
	Factor, Offset  decimal.Decimal
	ProductScoped   bool
}

// Identifier is a GS1 or custom product code.
type Identifier struct {
	ProductID string
	UOM       string
	Type      string
	Value     string
	Primary   bool
}

// DocumentInfoRecord is a DIR (FR-8.20).
type DocumentInfoRecord struct {
	TypeCode    string
	DocNumber   string
	Part        string
	DocVersion  string
	Description string
	Status      string
	Links       []DocumentLink
	Originals   []DocumentOriginal
}

// DocumentLink points a DIR at another object.
type DocumentLink struct {
	TargetType string
	TargetID   string
}

// DocumentOriginal is one file on a DIR version.
type DocumentOriginal struct {
	StorageKey string
	SHA256     string
	SizeBytes  int64
	MIME       string
	Text       string
}

// DocumentTypeSettings control originals and versions.
type DocumentTypeSettings struct {
	Code          string
	MaxOriginals  int
	AllowedMIMEs  []string
	VersionScheme string
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
	Code      string
	Name      string
	Hierarchy string
	Root      string
	ValidFrom string
	ValidTo   string
}
