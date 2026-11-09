// Package events holds the CloudEvent type names and payloads the pricing
// service publishes. Contract: events/asyncapi.yaml.
package events

// Event types. The kernel regex allows three segments after the namespace, so the
// catalog's condition-record.activated collapses to conditionrecord.activated (as org does).
const (
	ConditionRecordActivated = "dev.passage.pricing.conditionrecord.activated.v1"
	ConditionRecordWithdrawn = "dev.passage.pricing.conditionrecord.withdrawn.v1"
	JournalPosted            = "dev.passage.pricing.journal.posted.v1"
	RebateAccrualPosted      = "dev.passage.pricing.rebateaccrual.posted.v1"
	CommissionCalculated     = "dev.passage.pricing.commission.calculated.v1"
	// A commission settlement export was recorded for AP or payroll, and later voided (FR-26.18).
	CommissionExportIssued = "dev.passage.pricing.commissionexport.issued.v1"
	CommissionExportVoided = "dev.passage.pricing.commissionexport.voided.v1"
	// Trade promotion funds (FR-26.19, BR-26.15).
	FundClosed        = "dev.passage.pricing.fund.closed.v1"
	FundReopened      = "dev.passage.pricing.fund.reopened.v1"
	FundUsageRecorded = "dev.passage.pricing.fundusage.recorded.v1"
	// Campaigns (FR-26.10): the kill switch set (killed) or cleared (restored).
	CampaignKilled   = "dev.passage.pricing.campaign.killed.v1"
	CampaignRestored = "dev.passage.pricing.campaign.restored.v1"
	// CampaignRevised: name, validity, target customer group or budget changed.
	CampaignRevised = "dev.passage.pricing.campaign.revised.v1"
	// Pricing procedures (FR-26.1): a version became ACTIVE.
	ProcedureActivated = "dev.passage.pricing.procedure.activated.v1"
	// Rebate agreements (FR-26.11): revised, and closed early.
	RebateAgreementRevised = "dev.passage.pricing.rebateagreement.revised.v1"
	RebateAgreementClosed  = "dev.passage.pricing.rebateagreement.closed.v1"
	// RebateAgreementReopened: a CLOSED agreement became ACTIVE again with a later end date.
	RebateAgreementReopened = "dev.passage.pricing.rebateagreement.reopened.v1"
	// Vendor rebate claims (FR-26.15): a period claim was created, and approved for w19 AP.
	VendorRebateClaimCreated  = "dev.passage.pricing.vendorrebateclaim.created.v1"
	VendorRebateClaimApproved = "dev.passage.pricing.vendorrebateclaim.approved.v1"
	// Broker contracts and royalty agreements (FR-26.16, FR-26.17): created and closed.
	BrokerContractCreated   = "dev.passage.pricing.brokercontract.created.v1"
	BrokerContractClosed    = "dev.passage.pricing.brokercontract.closed.v1"
	RoyaltyAgreementCreated = "dev.passage.pricing.royaltyagreement.created.v1"
	RoyaltyAgreementClosed  = "dev.passage.pricing.royaltyagreement.closed.v1"
	// A broker or royalty accrual posted (invoice line, credit note line or minimum royalty
	// shortfall), and the period claim created and approved for w19 AP.
	ContractAccrualPosted = "dev.passage.pricing.contractaccrual.posted.v1"
	ContractClaimCreated  = "dev.passage.pricing.contractclaim.created.v1"
	ContractClaimApproved = "dev.passage.pricing.contractclaim.approved.v1"
	// Pricing parameters (PRD 5.26.13): a legal entity's parameters were changed.
	ParametersChanged = "dev.passage.pricing.parameters.changed.v1"
	// A tenant's parameters (cache lifetime, campaign kill switch effect) were changed.
	TenantParametersChanged = "dev.passage.pricing.tenantparameter.changed.v1"
	// Procedure determination (PRD 5.26.13): the procedure a legal entity and sales channel
	// price with was set or cleared.
	ProcedureAssignmentChanged = "dev.passage.pricing.procedureassignment.changed.v1"
)

// ProcedureAssignmentChangedData is the payload of procedureassignment.changed. Cleared is
// true when the assignment was removed (the channel falls back to the legal entity's default,
// or to the reference order when there is none).
type ProcedureAssignmentChangedData struct {
	LegalEntityID string `json:"legal_entity_id"`
	SalesChannel  string `json:"sales_channel"`
	ProcedureName string `json:"procedure_name"`
	Cleared       bool   `json:"cleared"`
	Version       int    `json:"version"`
	UpdatedBy     string `json:"updated_by"`
}

// ParametersChangedData is the payload of parameters.changed.
type ParametersChangedData struct {
	LegalEntityID   string `json:"legal_entity_id"`
	JournalFourEyes bool   `json:"journal_four_eyes"`
	// PricingDateBasis, TotalDiscountBasis and SmartRoundingBands are the values in force
	// after the change (the audit record of PRD 5.26.13 parameters).
	PricingDateBasis   string     `json:"pricing_date_basis"`
	TotalDiscountBasis string     `json:"total_discount_basis"`
	SmartRoundingBands []BandData `json:"smart_rounding_bands"`
	// PriceSearchMode and SmallOrderThresholds (currency to decimal string; empty for the
	// reference table) are the other two values of a legal entity.
	PriceSearchMode      string            `json:"price_search_mode"`
	SmallOrderThresholds map[string]string `json:"small_order_thresholds"`
	// FreeGoodsCalcRule is the calculation rule a new free goods rule takes when its create
	// request names none (BR-26.13).
	FreeGoodsCalcRule string `json:"free_goods_calc_rule"`
	Version           int    `json:"version"`
	UpdatedBy         string `json:"updated_by"`
}

// TenantParametersChangedData is the payload of tenantparameter.changed. CacheTTLSeconds is 0
// when the tenant follows the deployment's PRICING_CACHE_TTL.
type TenantParametersChangedData struct {
	CacheTTLSeconds          int    `json:"cache_ttl_seconds"`
	CampaignKillSwitchEffect string `json:"campaign_kill_switch_effect"`
	Version                  int    `json:"version"`
	UpdatedBy                string `json:"updated_by"`
}

// BandData is one smart rounding band; the last band has no up_to.
type BandData struct {
	UpTo   string `json:"up_to,omitempty"`
	Ending string `json:"ending"`
}

// VendorRebateClaimData is the payload of vendorrebateclaim.created and .approved. Amount
// is the period's accruals less its earlier claims (BR-26.11): w19 AP settles an approved
// positive claim by a credit note or a payment deduction (SettlementMode) and a negative
// one by a vendor invoice (VENDOR_INVOICE). Ids only, never vendor names.
type VendorRebateClaimData struct {
	ClaimID        string `json:"claim_id"`
	Number         string `json:"number"`
	LegalEntityID  string `json:"legal_entity_id"`
	AgreementID    string `json:"agreement_id"`
	VendorID       string `json:"vendor_id"`
	PeriodID       string `json:"period_id"`
	AccruedTotal   string `json:"accrued_total"`
	PriorClaimed   string `json:"prior_claimed"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	SettlementMode string `json:"settlement_mode"`
	Status         string `json:"status"`
	Version        int    `json:"version"`
}

// CampaignStatusData is the payload of campaign.killed and campaign.restored. A killed
// campaign's records leave the next resolve (NEXT_RESOLVE, §5.26.13); the sales cache
// drops its snapshot for the legal entity on either event.
type CampaignStatusData struct {
	CampaignID    string `json:"campaign_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Killed        bool   `json:"killed"`
	Version       int    `json:"version"`
}

// CampaignRevisedData is the payload of campaign.revised: the campaign's terms after the
// change. Validity and target group gate resolve, so caches drop their snapshot for the legal entity.
type CampaignRevisedData struct {
	CampaignID          string `json:"campaign_id"`
	LegalEntityID       string `json:"legal_entity_id"`
	Name                string `json:"name"`
	ValidFrom           string `json:"valid_from"`
	ValidTo             string `json:"valid_to"`
	TargetCustomerGroup string `json:"target_customer_group"`
	Budget              string `json:"budget"`
	Currency            string `json:"currency"`
	Killed              bool   `json:"killed"`
	Version             int    `json:"version"`
}

// ProcedureActivatedData is the payload of procedure.activated. SupersededID names the
// version it replaced, when there was one.
type ProcedureActivatedData struct {
	ProcedureID   string `json:"procedure_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Name          string `json:"name"`
	Version       int    `json:"version"`
	SupersededID  string `json:"superseded_id,omitempty"`
}

// FundStatusData is the payload of fund.closed and fund.reopened. Claims and
// deductions (w30) stop attributing to a closed fund.
type FundStatusData struct {
	FundID        string `json:"fund_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Closed        bool   `json:"closed"`
	Version       int    `json:"version"`
}

// FundUsageRecordedData is the payload of fundusage.recorded: one usage attribution with
// the period's usage and budget after it (BR-26.15; the Fund usage inquiry, §5.26.14).
type FundUsageRecordedData struct {
	UsageID       string `json:"usage_id"`
	FundID        string `json:"fund_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Period        string `json:"period"`
	Source        string `json:"source"`
	Reference     string `json:"reference"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	PeriodUsage   string `json:"period_usage"`
	Budget        string `json:"budget"`
}

// JournalPostedData is the payload of journal.posted (post and reverse, FR-26.9).
type JournalPostedData struct {
	JournalID     string `json:"journal_id"`
	LegalEntityID string `json:"legal_entity_id"`
	LineCount     int    `json:"line_count"`
	// ReversedJournalID is set when this journal reverses another.
	ReversedJournalID string `json:"reversed_journal_id,omitempty"`
}

// RebateAccrualPostedData is the payload of rebateaccrual.posted. The ledger (w11) posts
// Dr DebitAccount / Cr CreditAccount for Amount when it consumes the event: Dr 4150 /
// Cr 2150 for a CUSTOMER agreement (FR-26.11) and Dr 2160 / Cr 5100 for a VENDOR agreement
// (FR-26.15), unless the agreement names other accounts. Amount is negative for a credit
// note or a tier recompute that lowers the period.
type RebateAccrualPostedData struct {
	AccrualID     string `json:"accrual_id"`
	LegalEntityID string `json:"legal_entity_id"`
	AgreementID   string `json:"agreement_id"`
	PartyRole     string `json:"party_role"`
	PeriodID      string `json:"period_id"`
	InvoiceLineID string `json:"invoice_line_id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	DebitAccount  string `json:"debit_account"`
	CreditAccount string `json:"credit_account"`
	// Retroactive is true for the per-period delta of a retroactive recompute (BR-26.7, FR-26.23).
	Retroactive bool `json:"retroactive"`
}

// CommissionCalculatedData is the payload of commission.calculated (FR-26.12, FR-26.18):
// one rep's commission on one invoice line. The ledger (w11) posts Dr DebitAccount /
// Cr CreditAccount for Amount (6200 / 2170); Amount is negative on a credit note, which
// names the line it reverses. It carries the rep id only: rep names never appear in
// events (PRD §5.26.20).
type CommissionCalculatedData struct {
	TransactionID         string `json:"transaction_id"`
	LegalEntityID         string `json:"legal_entity_id"`
	InvoiceLineID         string `json:"invoice_line_id"`
	ReversesInvoiceLineID string `json:"reverses_invoice_line_id,omitempty"`
	SalesGroupID          string `json:"sales_group_id"`
	SalesRepID            string `json:"sales_rep_id"`
	Basis                 string `json:"basis"`
	BasisAmount           string `json:"basis_amount"`
	Rate                  string `json:"rate"`
	Amount                string `json:"amount"`
	Currency              string `json:"currency"`
	PeriodID              string `json:"period_id"`
	DebitAccount          string `json:"debit_account"`
	CreditAccount         string `json:"credit_account"`
}

// ConditionRecordData is the payload of conditionrecord.activated. Keys are
// carried, never customer names or personal data.
type ConditionRecordData struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legal_entity_id"`
	Type          string `json:"type"`
	CustomerKind  string `json:"customer_kind"`
	CustomerKey   string `json:"customer_key"`
	ProductKind   string `json:"product_kind"`
	ProductKey    string `json:"product_key"`
	Variant       string `json:"variant"`
	Currency      string `json:"currency"`
	Unit          string `json:"unit"`
	FromQuantity  string `json:"from_quantity"`
	ValidFrom     string `json:"valid_from"`
	ValidTo       string `json:"valid_to"`
	Version       int    `json:"version"`
	// Retroactive is true when valid_from is before the activation day (BR-26.7).
	Retroactive bool `json:"retroactive"`
	// JournalID names the price journal that posted or reversed the record; empty for a single activation.
	JournalID string `json:"journal_id,omitempty"`
	// CampaignID names the campaign the record belongs to (FR-26.10); empty when it stands alone.
	CampaignID string `json:"campaign_id,omitempty"`
}

// ConditionRecordWithdrawnData is the payload of conditionrecord.withdrawn.
type ConditionRecordWithdrawnData struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Reason  string `json:"reason"`
}

// Free goods rules (FR-26.22): a rule was created, revised or withdrawn. The sales cache drops
// its free goods snapshot for the legal entity on any of them.
const (
	FreeGoodsRuleCreated   = "dev.passage.pricing.freegoodsrule.created.v1"
	FreeGoodsRuleWithdrawn = "dev.passage.pricing.freegoodsrule.withdrawn.v1"
	FreeGoodsRuleRevised   = "dev.passage.pricing.freegoodsrule.revised.v1"
)

// FreeGoodsRuleData is the payload of freegoodsrule.created, .revised and .withdrawn.
type FreeGoodsRuleData struct {
	RuleID        string `json:"rule_id"`
	LegalEntityID string `json:"legal_entity_id"`
	CustomerKind  string `json:"customer_kind"`
	CustomerKey   string `json:"customer_key"`
	ProductKind   string `json:"product_kind"`
	ProductID     string `json:"product_id"`
	Unit          string `json:"unit"`
	Kind          string `json:"kind"`
	CalcRule      string `json:"calc_rule"`
	EveryQuantity string `json:"every_quantity"`
	FreeQuantity  string `json:"free_quantity"`
	MinQuantity   string `json:"min_quantity"`
	ValidFrom     string `json:"valid_from"`
	ValidTo       string `json:"valid_to"`
	Withdrawn     bool   `json:"withdrawn"`
	Version       int    `json:"version"`
}

// RebateAgreementData is the payload of rebateagreement.revised and .closed. Ids only. After
// a close, ValidTo is the close date and no invoice dated later accrues (BR-26.8).
type RebateAgreementData struct {
	AgreementID   string `json:"agreement_id"`
	LegalEntityID string `json:"legal_entity_id"`
	PartyRole     string `json:"party_role"`
	Status        string `json:"status"`
	ValidFrom     string `json:"valid_from"`
	ValidTo       string `json:"valid_to"`
	Version       int    `json:"version"`
	// ProductID or ProductGroupID is the agreement's product scope, absent for an agreement
	// that takes every invoice line.
	ProductID      string `json:"product_id,omitempty"`
	ProductGroupID string `json:"product_group_id,omitempty"`
}

// Commodity pricing (FR-26.21). A basis, an index quote, a price calculation and a completed
// run each announce themselves; the run's journal is announced by journal posting.
const (
	CommodityBasisCreated         = "dev.passage.pricing.commoditybasis.created.v1"
	CommodityIndexRecorded        = "dev.passage.pricing.commodityindex.recorded.v1"
	CommodityCalculationCreated   = "dev.passage.pricing.commoditycalculation.created.v1"
	CommodityCalculationWithdrawn = "dev.passage.pricing.commoditycalculation.withdrawn.v1"
	CommodityRunCompleted         = "dev.passage.pricing.commodityrun.completed.v1"
)

// CommodityBasisData is the payload of commoditybasis.created.
type CommodityBasisData struct {
	BasisID       string `json:"basis_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Code          string `json:"code"`
	Currency      string `json:"currency"`
	Unit          string `json:"unit"`
}

// CommodityIndexRecordedData is the payload of commodityindex.recorded.
type CommodityIndexRecordedData struct {
	BasisID       string `json:"basis_id"`
	LegalEntityID string `json:"legal_entity_id"`
	AsOf          string `json:"as_of"`
	Revision      int    `json:"revision"`
	Value         string `json:"value"`
	Source        string `json:"source"`
}

// CommodityCalculationData is the payload of commoditycalculation.created and .withdrawn.
type CommodityCalculationData struct {
	CalculationID string `json:"calculation_id"`
	LegalEntityID string `json:"legal_entity_id"`
	BasisID       string `json:"basis_id"`
	ProductID     string `json:"product_id"`
	CustomerKind  string `json:"customer_kind"`
	CustomerKey   string `json:"customer_key"`
	Unit          string `json:"unit"`
	Currency      string `json:"currency"`
	Factor        string `json:"factor"`
	MarginKind    string `json:"margin_kind"`
	Margin        string `json:"margin"`
	Withdrawn     bool   `json:"withdrawn"`
	Version       int    `json:"version"`
}

// CommodityRunCompletedData is the payload of commodityrun.completed. journal_id is absent
// when no record changed price.
type CommodityRunCompletedData struct {
	RunID          string `json:"run_id"`
	CalculationID  string `json:"calculation_id"`
	LegalEntityID  string `json:"legal_entity_id"`
	AsOf           string `json:"as_of"`
	IndexAsOf      string `json:"index_as_of"`
	IndexRevision  int    `json:"index_revision"`
	IndexValue     string `json:"index_value"`
	Price          string `json:"price"`
	ChangedCount   int    `json:"changed_count"`
	UnchangedCount int    `json:"unchanged_count"`
	Stale          bool   `json:"stale"`
	JournalID      string `json:"journal_id,omitempty"`
}

// BrokerContractData is the payload of brokercontract.created and .closed. Ids only.
type BrokerContractData struct {
	ContractID     string `json:"contract_id"`
	LegalEntityID  string `json:"legal_entity_id"`
	BrokerVendorID string `json:"broker_vendor_id"`
	RateKind       string `json:"rate_kind"`
	Rate           string `json:"rate"`
	Currency       string `json:"currency"`
	ValidFrom      string `json:"valid_from"`
	ValidTo        string `json:"valid_to"`
	Status         string `json:"status"`
	Version        int    `json:"version"`
}

// RoyaltyAgreementData is the payload of royaltyagreement.created and .closed. Ids only.
type RoyaltyAgreementData struct {
	AgreementID      string `json:"agreement_id"`
	LegalEntityID    string `json:"legal_entity_id"`
	LicensorVendorID string `json:"licensor_vendor_id"`
	Basis            string `json:"basis"`
	Period           string `json:"period"`
	MinimumRoyalty   string `json:"minimum_royalty"`
	Currency         string `json:"currency"`
	ValidFrom        string `json:"valid_from"`
	ValidTo          string `json:"valid_to"`
	Status           string `json:"status"`
	Version          int    `json:"version"`
}

// ContractAccrualPostedData is the payload of contractaccrual.posted: one broker or royalty
// accrual (negative for a credit note line) for the ledger, Dr 6210 / Cr 2175 (broker) or
// Dr 6220 / Cr 2176 (royalty) by default. Kind is ACCRUAL, CREDIT or SHORTFALL.
type ContractAccrualPostedData struct {
	AccrualID     string `json:"accrual_id"`
	LegalEntityID string `json:"legal_entity_id"`
	ContractKind  string `json:"contract_kind"`
	ContractID    string `json:"contract_id"`
	VendorID      string `json:"vendor_id"`
	PeriodID      string `json:"period_id"`
	LineKey       string `json:"line_key"`
	Kind          string `json:"kind"`
	PostingDate   string `json:"posting_date"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	DebitAccount  string `json:"debit_account"`
	CreditAccount string `json:"credit_account"`
}

// ContractClaimData is the payload of contractclaim.created and .approved. Amount is the
// period's accruals less its earlier claims: w19 AP pays an approved positive claim to the
// vendor and offsets a negative one against the vendor's next payment. Ids only.
type ContractClaimData struct {
	ClaimID       string `json:"claim_id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legal_entity_id"`
	ContractKind  string `json:"contract_kind"`
	ContractID    string `json:"contract_id"`
	VendorID      string `json:"vendor_id"`
	PeriodID      string `json:"period_id"`
	AccruedTotal  string `json:"accrued_total"`
	PriorClaimed  string `json:"prior_claimed"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	Version       int    `json:"version"`
}

// CommissionExportTotal is the amount an export hands over in one currency.
type CommissionExportTotal struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// CommissionExportData is the payload of commissionexport.issued and commissionexport.voided
// (FR-26.18). It carries totals per currency, never rep ids or names: the file itself goes to
// AP or payroll through the download. VoidReason is set on voided only.
type CommissionExportData struct {
	ExportID      string                  `json:"export_id"`
	LegalEntityID string                  `json:"legal_entity_id"`
	Target        string                  `json:"target"`
	PeriodFrom    string                  `json:"period_from"`
	PeriodTo      string                  `json:"period_to"`
	LineCount     int                     `json:"line_count"`
	Totals        []CommissionExportTotal `json:"totals"`
	CSVSHA256     string                  `json:"csv_sha256"`
	Version       int                     `json:"version"`
	UserID        string                  `json:"user_id"`
	VoidReason    string                  `json:"void_reason,omitempty"`
}
