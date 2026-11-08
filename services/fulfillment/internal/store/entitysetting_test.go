package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func TestLegalEntitySetting_DefaultSaveVersionReplayAndEvent(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 3)

	// Nothing saved: both false at version 0, and the entity is listed from its shipment.
	got, err := s.GetLegalEntitySetting(ctx, f.legalEntity)
	if err != nil || got.Version != 0 || got.CogsAtShipment || got.AsnEnabled {
		t.Fatalf("default = %+v, %v", got, err)
	}
	if list, err := s.ListLegalEntitySettings(ctx); err != nil || len(list) != 1 || list[0].LegalEntityID != f.legalEntity || list[0].Version != 0 {
		t.Fatalf("default list = %+v, %v", list, err)
	}
	if _, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 1, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, idem("early")); problemCode(err) != store.CodeVersion {
		t.Fatalf("save at a version that is not 0: %v", err)
	}
	key := idem("first")
	saved, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, key)
	if err != nil || saved.Version != 1 || !saved.CogsAtShipment || saved.AsnEnabled || saved.UpdatedAt == nil {
		t.Fatalf("first save = %+v, %v", saved, err)
	}
	if replay, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, key); err != nil || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := countEvents(t, admin, fulevents.LegalEntitySettingChanged); n != 1 {
		t.Fatalf("events after a save and its replay = %d, want 1", n)
	}
	if _, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{AsnEnabled: true}, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale save: %v", err)
	}
	next, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 1, store.LegalEntitySettingInput{CogsAtShipment: false, AsnEnabled: true, CanChangeCogs: true}, idem("next"))
	if err != nil || next.Version != 2 || next.CogsAtShipment || !next.AsnEnabled {
		t.Fatalf("second save = %+v, %v", next, err)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{previous_cogs_at_shipment}"); got != "true" {
		t.Fatalf("previous cogs in event = %q, want true", got)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{asn_enabled}"); got != "true" {
		t.Fatalf("asn in event = %q, want true", got)
	}

	// Another tenant sees the default and saves its own row for the same id.
	if got, err := s.GetLegalEntitySetting(ctxFor(2, ""), f.legalEntity); err != nil || got.Version != 0 || got.AsnEnabled {
		t.Fatalf("other tenant get = %+v, %v", got, err)
	}
	if got, err := s.SaveLegalEntitySetting(ctxFor(2, ""), f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, idem("t2")); err != nil || got.Version != 1 {
		t.Fatalf("other tenant save = %+v, %v", got, err)
	}
	if got, _ := s.GetLegalEntitySetting(ctx, f.legalEntity); !got.AsnEnabled || got.CogsAtShipment {
		t.Fatalf("tenant 1 setting changed by tenant 2: %+v", got)
	}
}

// setCogsPolicy saves cogs_at_shipment for a legal entity (with the ledger
// setup duty) unless it already has that value, keeping asn_enabled.
func setCogsPolicy(t *testing.T, s *store.Postgres, ctx context.Context, legalEntity string, on bool) {
	t.Helper()
	cur, err := s.GetLegalEntitySetting(ctx, legalEntity)
	if err != nil {
		t.Fatal(err)
	}
	if cur.CogsAtShipment == on {
		return
	}
	if _, err := s.SaveLegalEntitySetting(ctx, legalEntity, cur.Version,
		store.LegalEntitySettingInput{CogsAtShipment: on, AsnEnabled: cur.AsnEnabled, CanChangeCogs: true}, idem("cogs-policy")); err != nil {
		t.Fatal(err)
	}
}

// Changing cogs_at_shipment is an accounting policy change: fulfillment.warehouse.release
// alone may change asn_enabled but not cogs_at_shipment.
func TestLegalEntitySetting_CogsChangeNeedsTheLedgerSetupDuty(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 3)
	if _, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true}, idem("no-duty")); problemCode(err) != store.CodeDutyMissing {
		t.Fatalf("cogs change without the duty: %v, want %s", err, store.CodeDutyMissing)
	}
	if n := countEvents(t, admin, fulevents.LegalEntitySettingChanged); n != 0 {
		t.Fatalf("events after a refused save = %d", n)
	}
	asn, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{AsnEnabled: true}, idem("asn-only"))
	if err != nil || asn.Version != 1 || !asn.AsnEnabled || asn.CogsAtShipment {
		t.Fatalf("asn change without the duty = %+v, %v", asn, err)
	}
	cogs, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 1, store.LegalEntitySettingInput{CogsAtShipment: true, AsnEnabled: true, CanChangeCogs: true}, idem("finance"))
	if err != nil || cogs.Version != 2 || !cogs.CogsAtShipment {
		t.Fatalf("cogs change with the duty = %+v, %v", cogs, err)
	}
	// Keeping cogs_at_shipment as it is needs no duty.
	if got, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 2, store.LegalEntitySettingInput{CogsAtShipment: true}, idem("keep")); err != nil || got.AsnEnabled || !got.CogsAtShipment {
		t.Fatalf("asn off keeping cogs = %+v, %v", got, err)
	}
}

func TestLegalEntitySetting_Scope(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 3)
	other := ids.New()
	if _, err := s.SaveLegalEntitySetting(ctx, f.legalEntity, 0, store.LegalEntitySettingInput{AsnEnabled: true}, idem("ok")); err != nil {
		t.Fatal(err)
	}

	// A principal scoped to another legal entity sees neither the entity nor its
	// setting, and cannot save one for it.
	scoped := ctxFor(1, other)
	if list, err := s.ListLegalEntitySettings(scoped); err != nil || len(list) != 0 {
		t.Fatalf("other legal entity list = %+v, %v", list, err)
	}
	if got, err := s.GetLegalEntitySetting(scoped, f.legalEntity); err != nil || got.Version != 0 || got.AsnEnabled {
		t.Fatalf("other legal entity get = %+v, %v", got, err)
	}
	if _, err := s.SaveLegalEntitySetting(scoped, f.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, CanChangeCogs: true}, idem("sneak")); problemCode(err) != store.CodeLegalEntityScope {
		t.Fatalf("save outside the scope: %v, want %s", err, store.CodeLegalEntityScope)
	}
	own := ctxFor(1, f.legalEntity)
	if got, err := s.GetLegalEntitySetting(own, f.legalEntity); err != nil || !got.AsnEnabled || got.Version != 1 {
		t.Fatalf("own legal entity get = %+v, %v", got, err)
	}
	if list, _ := s.ListLegalEntitySettings(own); len(list) != 1 {
		t.Fatalf("own list = %+v", list)
	}
}

// A post that names neither cogsAtShipment nor asnEnabled takes the settings of
// the shipment's legal entity; a named value wins over the setting.
func TestPostPackingSlip_TakesTheLegalEntitySettings(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")

	// No saved setting: Dr 1405, no ASN, as before this table existed.
	plain := released(t, s, ctx, 3)
	packed := pickAndPack(t, s, ctx, plain.shipment)
	slip, err := s.PostPackingSlip(ctx, plain.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsFromSetting: true, AsnFromSetting: true, Costs: costs(plain.shipment, "2")}, idem("plain"))
	if err != nil || slip.Journal[0].Account != "1405" {
		t.Fatalf("default post = %+v, %v", slip.Journal, err)
	}
	if n := countEvents(t, admin, fulevents.ASNIssued); n != 0 {
		t.Fatalf("asn events without a setting = %d", n)
	}

	set := released(t, s, ctx, 3)
	if _, err := s.SaveLegalEntitySetting(ctx, set.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, AsnEnabled: true, CanChangeCogs: true}, idem("set")); err != nil {
		t.Fatal(err)
	}
	packed = pickAndPack(t, s, ctx, set.shipment)
	slip, err = s.PostPackingSlip(ctx, set.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsFromSetting: true, AsnFromSetting: true, Costs: costs(set.shipment, "2")}, idem("set-post"))
	if err != nil || slip.Journal[0].Account != "5000" {
		t.Fatalf("post with COGS at shipment set = %+v, %v", slip.Journal, err)
	}
	if n := countEvents(t, admin, fulevents.ASNIssued); n != 1 {
		t.Fatalf("asn events with the setting = %d, want 1", n)
	}

	// A named asnEnabled wins over the setting; a named cogsAtShipment must
	// match the policy (FUL-0059) and posts when it does.
	named := released(t, s, ctx, 3)
	if _, err := s.SaveLegalEntitySetting(ctx, named.legalEntity, 0, store.LegalEntitySettingInput{CogsAtShipment: true, AsnEnabled: true, CanChangeCogs: true}, idem("named")); err != nil {
		t.Fatal(err)
	}
	packed = pickAndPack(t, s, ctx, named.shipment)
	if _, err := s.PostPackingSlip(ctx, named.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", AsnFromSetting: true, Costs: costs(named.shipment, "2")}, idem("contradicts")); problemCode(err) != store.CodeCogsPolicy {
		t.Fatalf("post naming cogsAtShipment=false against the policy: %v, want %s", err, store.CodeCogsPolicy)
	}
	if n := countEvents(t, admin, fulevents.PackingSlipPosted); n != 2 {
		t.Fatalf("slips after a refused post = %d, want 2", n)
	}
	slip, err = s.PostPackingSlip(ctx, named.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsAtShipment: true, Costs: costs(named.shipment, "2")}, idem("named-post"))
	if err != nil || slip.Journal[0].Account != "5000" {
		t.Fatalf("post naming the policy value = %+v, %v", slip.Journal, err)
	}
	if n := countEvents(t, admin, fulevents.ASNIssued); n != 1 {
		t.Fatalf("asn events after a post naming asnEnabled=false = %d, want still 1", n)
	}

	// A post without a saved setting that names cogsAtShipment=true is refused
	// too: the default policy is physical posting (Dr 1405).
	unset := released(t, s, ctx, 3)
	packed = pickAndPack(t, s, ctx, unset.shipment)
	if _, err := s.PostPackingSlip(ctx, unset.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsAtShipment: true, Costs: costs(unset.shipment, "2")}, idem("unset")); problemCode(err) != store.CodeCogsPolicy {
		t.Fatalf("post naming cogsAtShipment=true without a policy: %v", err)
	}
}

func str(v string) *string { return &v }
func num(v int) *int       { return &v }

// BR-28.5: the containers of a legal entity with its own GS1 company prefix and
// extension digit carry SSCCs under them; an entity without one keeps the
// process default, and a change of prefix leaves the SSCCs already issued as
// they were. BR-28.12: the slip remembers packaging_fee_billable and the
// posted event carries it.
func TestLegalEntitySetting_GS1PrefixAndBillableFee(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")

	own := released(t, s, ctx, 3)
	prefix := "9876543"
	saved, err := s.SaveLegalEntitySetting(ctx, own.legalEntity, 0, store.LegalEntitySettingInput{
		GS1CompanyPrefix: store.Change[*string]{Set: true, Value: str(prefix)}, SSCCExtensionDigit: store.Change[*int]{Set: true, Value: num(5)},
		PackagingFeeBillable: store.Change[bool]{Set: true, Value: true}}, idem("gs1"))
	if err != nil || deref(saved.GS1CompanyPrefix) != prefix || saved.SSCCExtensionDigit == nil || *saved.SSCCExtensionDigit != 5 || !saved.PackagingFeeBillable {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{gs1_company_prefix}"); got != prefix {
		t.Fatalf("gs1 prefix in event = %q", got)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{sscc_extension_digit}"); got != "5" {
		t.Fatalf("extension digit in event = %q", got)
	}
	if got, err := s.GetLegalEntitySetting(ctx, own.legalEntity); err != nil || deref(got.GS1CompanyPrefix) != prefix {
		t.Fatalf("read back = %+v, %v", got, err)
	}
	packed := pickAndPack(t, s, ctx, own.shipment)
	if len(packed.Shipment.Containers) == 0 {
		t.Fatal("no container packed")
	}
	first := packed.Shipment.Containers[0].SSCC
	for _, c := range packed.Shipment.Containers {
		if err := domain.ValidSSCC(c.SSCC); err != nil || c.SSCC[:8] != "59876543" {
			t.Fatalf("SSCC %s under the entity prefix: %v", c.SSCC, err)
		}
	}
	slip, err := s.PostPackingSlip(ctx, own.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsFromSetting: true, Costs: costs(own.shipment, "2")}, idem("gs1-post"))
	if err != nil || !slip.PackagingFeeBillable {
		t.Fatalf("post = %+v, %v", slip, err)
	}
	if got := outboxData(t, admin, fulevents.PackingSlipPosted, "{packaging_fee_billable}"); got != "true" {
		t.Fatalf("billable in posted event = %q", got)
	}

	// A save that leaves the three out (a client that predates them) keeps them.
	cur, _ := s.GetLegalEntitySetting(ctx, own.legalEntity)
	kept, err := s.SaveLegalEntitySetting(ctx, own.legalEntity, cur.Version, store.LegalEntitySettingInput{AsnEnabled: true}, idem("old-client"))
	if err != nil || deref(kept.GS1CompanyPrefix) != prefix || kept.SSCCExtensionDigit == nil || *kept.SSCCExtensionDigit != 5 || !kept.PackagingFeeBillable || !kept.AsnEnabled {
		t.Fatalf("save without the new fields = %+v, %v", kept, err)
	}

	// Turning the flag off and changing the prefix: the slip already posted and
	// the SSCCs already issued stay as they were; the reversing slip repeats the flag.
	newPrefix := "4512345"
	changed, err := s.SaveLegalEntitySetting(ctx, own.legalEntity, kept.Version, store.LegalEntitySettingInput{AsnEnabled: true,
		GS1CompanyPrefix: store.Change[*string]{Set: true, Value: str(newPrefix)}, PackagingFeeBillable: store.Change[bool]{Set: true}}, idem("off"))
	if err != nil || deref(changed.GS1CompanyPrefix) != newPrefix || *changed.SSCCExtensionDigit != 5 || changed.PackagingFeeBillable {
		t.Fatalf("change = %+v, %v", changed, err)
	}
	if got := outboxData(t, admin, fulevents.LegalEntitySettingChanged, "{previous_gs1_company_prefix}"); got != prefix {
		t.Fatalf("previous prefix in event = %q", got)
	}
	reread, err := s.GetShipment(ctx, own.shipment.ID)
	if err != nil || reread.Containers[0].SSCC != first {
		t.Fatalf("SSCC after the prefix change = %+v, %v; want %s", reread.Containers, err, first)
	}
	rev, err := s.CancelPackingSlip(ctx, slip.ID, idem("gs1-cancel"))
	if err != nil || !rev.PackagingFeeBillable {
		t.Fatalf("cancel = %+v, %v", rev, err)
	}
	next := released(t, s, ctx, 1)
	keepPrefix(t, s, ctx, next.legalEntity, newPrefix)
	if c := pickAndPack(t, s, ctx, next.shipment).Shipment.Containers; c[0].SSCC[:8] != "04512345" {
		t.Fatalf("SSCC under the default extension = %s", c[0].SSCC)
	}

	// Null returns the entity to the process prefix.
	cur, _ = s.GetLegalEntitySetting(ctx, own.legalEntity)
	reset, err := s.SaveLegalEntitySetting(ctx, own.legalEntity, cur.Version, store.LegalEntitySettingInput{AsnEnabled: true,
		GS1CompanyPrefix: store.Change[*string]{Set: true}, SSCCExtensionDigit: store.Change[*int]{Set: true}}, idem("reset"))
	if err != nil || reset.GS1CompanyPrefix != nil || reset.SSCCExtensionDigit != nil {
		t.Fatalf("reset = %+v, %v", reset, err)
	}

	// An entity with no setting keeps the process prefix and posts a non-billable slip.
	plain := released(t, s, ctx, 3)
	packed = pickAndPack(t, s, ctx, plain.shipment)
	for _, c := range packed.Shipment.Containers {
		if c.SSCC[:8] != "00614141" {
			t.Fatalf("SSCC %s without a setting, want the process prefix", c.SSCC)
		}
	}
	slip, err = s.PostPackingSlip(ctx, plain.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", CogsFromSetting: true, Costs: costs(plain.shipment, "2")}, idem("plain-post"))
	if err != nil || slip.PackagingFeeBillable {
		t.Fatalf("plain post = %+v, %v", slip, err)
	}
}

// keepPrefix saves prefix for legalEntity with the process extension digit.
func keepPrefix(t *testing.T, s *store.Postgres, ctx context.Context, legalEntity, prefix string) {
	t.Helper()
	cur, err := s.GetLegalEntitySetting(ctx, legalEntity)
	if err == nil {
		_, err = s.SaveLegalEntitySetting(ctx, legalEntity, cur.Version, store.LegalEntitySettingInput{
			GS1CompanyPrefix: store.Change[*string]{Set: true, Value: str(prefix)}}, idem("prefix"))
	}
	if err != nil {
		t.Fatal(err)
	}
}

// Nested GS1 company prefixes would issue the same SSCCs from their separate
// counters (an 8-digit prefix and its 7-digit head), so a save that nests
// with the process prefix or another entity's is refused; an equal prefix
// shares the counter and never repeats an SSCC.
func TestLegalEntitySetting_NestedPrefixRefusedSharedPrefixShares(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	a, b, c := released(t, s, ctx, 1), released(t, s, ctx, 1), released(t, s, ctx, 1)

	for _, nested := range []string{"06141410", "061414"} {
		_, err := s.SaveLegalEntitySetting(ctx, a.legalEntity, 0, store.LegalEntitySettingInput{
			GS1CompanyPrefix: store.Change[*string]{Set: true, Value: str(nested)}}, idem("nest-"+nested))
		if problemCode(err) != store.CodeValidation {
			t.Fatalf("prefix %s nesting with the process prefix: %v", nested, err)
		}
	}
	keepPrefix(t, s, ctx, a.legalEntity, "7012345")
	_, err := s.SaveLegalEntitySetting(ctx, b.legalEntity, 0, store.LegalEntitySettingInput{
		GS1CompanyPrefix: store.Change[*string]{Set: true, Value: str("70123456")}}, idem("nest-other"))
	if problemCode(err) != store.CodeValidation {
		t.Fatalf("prefix nesting with another entity's: %v", err)
	}
	keepPrefix(t, s, ctx, b.legalEntity, "7012345")
	keepPrefix(t, s, ctx, c.legalEntity, "0614141")
	seen := map[string]bool{}
	for _, f := range []fixture{a, b, c} {
		for _, cont := range pickAndPack(t, s, ctx, f.shipment).Shipment.Containers {
			if seen[cont.SSCC[1:]] {
				t.Fatalf("SSCC body %s issued twice", cont.SSCC[1:])
			}
			seen[cont.SSCC[1:]] = true
		}
	}
}

// A 12-digit prefix leaves 9,999 serial references: once used up the pack is
// refused with a validation problem naming the prefix, not a server error. An
// SSCC issued before (a counter that ran behind) is FUL-0007.
func TestPack_SSCCSerialsUsedUpOrRepeatedAreRefused(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 3)
	keepPrefix(t, s, ctx, f.legalEntity, "123456789012")
	counter := func(next int) {
		t.Helper()
		if _, err := admin.Exec(context.Background(), `insert into number_counter (tenant_id, sequence_id, next_value) values ($1, 'sscc:123456789012', $2)
			on conflict (tenant_id, sequence_id) do update set next_value = excluded.next_value`, testkit.TenantID(1), next); err != nil {
			t.Fatal(err)
		}
	}
	l := f.shipment.Lines[0]
	sh, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: l.ID, Picked: l.Planned}}, idem("pick"))
	if err != nil {
		t.Fatal(err)
	}
	one := []store.PackInput{{ShipmentLineID: l.ID, Qty: decimal.NewFromInt(1)}}
	counter(10000)
	if _, err := s.PackContainer(ctx, sh.ID, sh.Version, "CARTON-C12", one, idem("used-up")); problemCode(err) != store.CodeValidation {
		t.Fatalf("pack with the serials used up: %v", err)
	}
	counter(9999)
	packed, err := s.PackContainer(ctx, sh.ID, sh.Version, "CARTON-C12", one, idem("last"))
	if err != nil || packed.Shipment.Containers[0].SSCC[1:17] != "1234567890129999" {
		t.Fatalf("pack with the last serial: %+v, %v", packed.Shipment.Containers, err)
	}
	counter(9999)
	if _, err := s.PackContainer(ctx, sh.ID, packed.Shipment.Version, "CARTON-C12", one, idem("repeat")); problemCode(err) != store.CodeSSCCInvalid {
		t.Fatalf("pack repeating an SSCC: %v", err)
	}
}
