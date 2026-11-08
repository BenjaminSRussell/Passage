package api

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

// The package carries four more kinds of tenant data beside the structure records (FR-6.12):
// tenant units of measure and their conversions, currency rounding rules, and name
// translations. Records name each other by code, never by id, so a package moves between tenants.
const (
	kindUnit        = "units"
	kindConversion  = "conversions"
	kindRounding    = "currencyRoundingRules"
	kindTranslation = "nameTranslations"

	ownerLegalEntity   = "legal_entity"
	ownerOperatingUnit = "operating_unit"
	ownerSite          = "site"
	ownerWarehouse     = "warehouse"
)

// packageUnit is a tenant unit of measure; the system units are not exported.
type packageUnit struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	Class            string `json:"class"`
	DecimalPrecision *int   `json:"decimalPrecision"`
}

// packageConversion is a tenant conversion between two units of one class; factor and offset
// are decimal strings, an empty offset is zero.
type packageConversion struct {
	FromCode string `json:"fromCode"`
	ToCode   string `json:"toCode"`
	Factor   string `json:"factor"`
	Offset   string `json:"offset,omitempty"`
}

// packageRounding is a currency rounding rule; LegalEntity (a code) is empty for the tenant-wide rule.
type packageRounding struct {
	Currency    string `json:"currency"`
	Purpose     string `json:"purpose"`
	LegalEntity string `json:"legalEntity,omitempty"`
	Unit        string `json:"unit"`
	Method      string `json:"method"`
}

// packageTranslation is the name of a legal entity, operating unit, site or warehouse in one
// language. Owner is the record's natural key: the code of a legal entity, type/code of an
// operating unit, LEGAL-ENTITY/CODE of a site or warehouse.
type packageTranslation struct {
	OwnerType string `json:"ownerType"`
	Owner     string `json:"owner"`
	Language  string `json:"language"`
	Name      string `json:"name"`
}

// normalizeOwner returns the canonical natural key of a translation owner, false when the
// owner type is unknown or the key is not in the shape of that type.
func normalizeOwner(ownerType, owner string) (string, bool) {
	left, right, split := strings.Cut(owner, "/")
	switch ownerType {
	case ownerLegalEntity:
		return strings.ToUpper(owner), !split && owner != ""
	case ownerOperatingUnit:
		return left + "/" + strings.ToUpper(right), split && slices.Contains(operatingUnitTypes, left) && right != ""
	case ownerSite, ownerWarehouse:
		return strings.ToUpper(left) + "/" + strings.ToUpper(right), split && left != "" && right != "" && !strings.Contains(right, "/")
	}
	return owner, false
}

func exportReferenceData(ctx context.Context, st store.Store, entityCodes, refs map[string]string, pkg *OrgPackage) error {
	pkg.Units, pkg.Conversions, pkg.CurrencyRoundingRules, pkg.NameTranslations = []packageUnit{}, []packageConversion{}, []packageRounding{}, []packageTranslation{}
	units, err := st.ListUnits(ctx, store.UnitFilter{})
	if err != nil {
		return err
	}
	for _, u := range units {
		if !u.System {
			precision := u.DecimalPrecision
			pkg.Units = append(pkg.Units, packageUnit{Code: u.Code, Name: u.Name, Class: u.Class, DecimalPrecision: &precision})
		}
	}
	convs, err := st.ListConversions(ctx, store.ConversionFilter{})
	if err != nil {
		return err
	}
	for _, c := range convs {
		if !c.System {
			out := packageConversion{FromCode: c.FromCode, ToCode: c.ToCode, Factor: c.Factor.String()}
			if !c.Offset.IsZero() {
				out.Offset = c.Offset.String()
			}
			pkg.Conversions = append(pkg.Conversions, out)
		}
	}
	rules, translations, err := listScopedReferenceData(ctx, st, entityCodes)
	if err != nil {
		return err
	}
	for _, r := range rules {
		out := packageRounding{Currency: r.CurrencyCode, Purpose: r.Purpose, Unit: r.Unit.String(), Method: r.Method}
		if r.LegalEntityID != nil {
			// A rule is only read under the scope of a listed legal entity, so its code is known;
			// were it not, the rule must not be exported as the tenant-wide one.
			code, ok := refs[ownerLegalEntity+"/"+*r.LegalEntityID]
			if !ok {
				continue
			}
			out.LegalEntity = code
		}
		pkg.CurrencyRoundingRules = append(pkg.CurrencyRoundingRules, out)
	}
	for _, t := range translations {
		if owner, ok := refs[t.OwnerType+"/"+t.OwnerID]; ok {
			pkg.NameTranslations = append(pkg.NameTranslations, packageTranslation{OwnerType: t.OwnerType, Owner: owner, Language: t.Language, Name: t.Name})
		}
	}
	sort.SliceStable(pkg.CurrencyRoundingRules, func(i, j int) bool {
		a, b := pkg.CurrencyRoundingRules[i], pkg.CurrencyRoundingRules[j]
		return a.Currency+"/"+a.Purpose+"/"+a.LegalEntity < b.Currency+"/"+b.Purpose+"/"+b.LegalEntity
	})
	sort.SliceStable(pkg.NameTranslations, func(i, j int) bool {
		a, b := pkg.NameTranslations[i], pkg.NameTranslations[j]
		return a.OwnerType+"/"+a.Owner+"/"+a.Language < b.OwnerType+"/"+b.Owner+"/"+b.Language
	})
	for kind, n := range map[string]int{"units": len(pkg.Units), "conversions": len(pkg.Conversions), "rounding rules": len(pkg.CurrencyRoundingRules), "name translations": len(pkg.NameTranslations)} {
		if n > packageRowCap {
			return tooLarge(kind)
		}
	}
	return nil
}

// listScopedReferenceData reads every rounding rule and name translation of the tenant. Rows of a
// legal entity are visible only under that entity's scope, so it reads once unscoped (rows of no
// entity) and once per legal entity, and merges the answers in a stable order.
func listScopedReferenceData(ctx context.Context, st store.Store, entityCodes map[string]string) ([]store.CurrencyRounding, []store.NameTranslation, error) {
	entityIDs := make([]string, 0, len(entityCodes))
	for id := range entityCodes {
		entityIDs = append(entityIDs, id)
	}
	sort.Slice(entityIDs, func(i, j int) bool { return entityCodes[entityIDs[i]] < entityCodes[entityIDs[j]] })
	seenRules, seenNames := map[string]bool{}, map[string]bool{}
	var rules []store.CurrencyRounding
	var names []store.NameTranslation
	for _, scope := range append([]string{""}, entityIDs...) {
		scoped := ctx
		if scope != "" {
			scoped = scopedTo(ctx, scope)
		}
		found, err := st.ListCurrencyRoundings(scoped, "")
		if err != nil {
			return nil, nil, err
		}
		for _, r := range found {
			if !seenRules[r.ID] {
				seenRules[r.ID] = true
				rules = append(rules, r)
			}
		}
		translations, err := st.ListNameTranslations(scoped, "", "")
		if err != nil {
			return nil, nil, err
		}
		for _, t := range translations {
			if key := translationKey(t.OwnerType, t.OwnerID, t.Language); !seenNames[key] {
				seenNames[key] = true
				names = append(names, t)
			}
		}
	}
	return rules, names, nil
}

func validatePackageReferenceData(pkg OrgPackage, p packageProblems, dup func(path, kind, key string)) {
	for i, u := range pkg.Units {
		path := indexPath(kindUnit, i)
		if !store.ValidUnitCode(u.Code) {
			p.add(path+".code", unitCodeRule)
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(u.Name)); n == 0 || n > maxNameLength {
			p.add(path+".name", unitNameRule)
		}
		if !slices.Contains(store.UnitClasses(), u.Class) {
			p.add(path+".class", "must be one of "+strings.Join(store.UnitClasses(), ", "))
		}
		if u.DecimalPrecision == nil || *u.DecimalPrecision < 0 || *u.DecimalPrecision > maxUnitPrecision {
			p.add(path+".decimalPrecision", unitPrecisionRule)
		}
		dup(path+".code", "unit", u.Code)
	}
	for i, c := range pkg.Conversions {
		path := indexPath(kindConversion, i)
		if !store.ValidUnitCode(c.FromCode) {
			p.add(path+".fromCode", unitCodeRule)
		}
		if !store.ValidUnitCode(c.ToCode) {
			p.add(path+".toCode", unitCodeRule)
		} else if c.FromCode == c.ToCode {
			p.add(path+".toCode", "must differ from fromCode")
		}
		if f, ok := parseConversionDecimal(c.Factor); !ok || !f.IsPositive() {
			p.add(path+".factor", "must be a decimal greater than 0 with at most 16 integer digits and 12 decimals")
		}
		if _, ok := conversionOffset(c); !ok {
			p.add(path+".offset", "must be a decimal with at most 16 integer digits and 12 decimals")
		}
		dup(path, "conversion", conversionPair(c.FromCode, c.ToCode))
	}
	for i, r := range pkg.CurrencyRoundingRules {
		path := indexPath(kindRounding, i)
		unit := r.Unit
		_, fields := parseRule(r.Purpose, putCurrencyRoundingRequest{Unit: &unit, Method: r.Method})
		for field, rule := range fields {
			p.add(path+"."+field, rule)
		}
		if len(r.Currency) != 3 || strings.ToUpper(r.Currency) != r.Currency {
			p.add(path+".currency", "must be an ISO 4217 code such as USD")
		}
		dup(path, "rounding rule", r.Currency+"/"+r.Purpose+"/"+strings.ToUpper(r.LegalEntity))
	}
	for i, t := range pkg.NameTranslations {
		path := indexPath(kindTranslation, i)
		owner, ok := normalizeOwner(t.OwnerType, t.Owner)
		if !ok {
			p.add(path+".owner", "ownerType must be legal_entity, operating_unit, site or warehouse and owner its code (type/code for an operating unit, LEGAL-ENTITY/CODE for a site or warehouse)")
		}
		if !languageTag.MatchString(t.Language) {
			p.add(path+".language", languageRule)
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(t.Name)); n == 0 || n > maxNameLength {
			p.add(path+".name", "must be 1 to 120 characters")
		}
		dup(path, "name translation", t.OwnerType+"/"+owner+"/"+t.Language)
	}
}

func conversionOffset(c packageConversion) (decimal.Decimal, bool) {
	if c.Offset == "" {
		return decimal.Zero, true
	}
	return parseConversionDecimal(c.Offset)
}

func conversionPair(from, to string) string {
	if from > to {
		from, to = to, from
	}
	return from + "|" + to
}

// loadReferenceData reads the tenant's units, conversions, rounding rules and translations
// and the currencies the rules name.
func (im *importer) loadReferenceData() error {
	im.refUnits, im.refConvs, im.roundings, im.trans, im.currencies = map[string]store.Unit{}, map[string]store.Conversion{}, map[string]store.CurrencyRounding{},
		map[string]store.NameTranslation{}, map[string]store.ReferenceItem{}
	units, err := im.st.ListUnits(im.ctx, store.UnitFilter{})
	if err != nil {
		return err
	}
	for _, u := range units {
		im.refUnits[u.Code] = u
	}
	convs, err := im.st.ListConversions(im.ctx, store.ConversionFilter{})
	if err != nil {
		return err
	}
	for _, c := range convs {
		im.refConvs[conversionPair(c.FromCode, c.ToCode)] = c
	}
	entityCodes := map[string]string{}
	for code, e := range im.entities {
		entityCodes[e.id] = code
	}
	rules, translations, err := listScopedReferenceData(im.ctx, im.st, entityCodes)
	if err != nil {
		return err
	}
	if len(rules) > packageRowCap {
		return tooLarge("rounding rules")
	}
	if len(translations) > packageRowCap {
		return tooLarge("name translations")
	}
	for _, r := range rules {
		im.roundings[roundingKey(r.CurrencyCode, r.Purpose, deref(r.LegalEntityID))] = r
	}
	for _, t := range translations {
		im.trans[translationKey(t.OwnerType, t.OwnerID, t.Language)] = t
	}
	currencies, err := im.st.ListReference(im.ctx, store.KindCurrency, store.ReferenceFilter{})
	if err != nil {
		return err
	}
	for _, c := range currencies {
		im.currencies[c.Code] = c
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func roundingKey(currency, purpose, legalEntityID string) string {
	return currency + "/" + purpose + "/" + legalEntityID
}

func translationKey(ownerType, ownerID, language string) string {
	return ownerType + "/" + ownerID + "/" + language
}

// ownerID returns the id of a translation owner the tenant has or this import created
// (a "dry:" id in a dry run); empty when there is none.
func (im *importer) ownerID(ownerType, owner string) string {
	switch ownerType {
	case ownerLegalEntity:
		return im.entities[owner].id
	case ownerOperatingUnit:
		return im.units[owner].id
	}
	entity, code, _ := strings.Cut(owner, "/")
	entityID := im.entities[entity].id
	if entityID == "" {
		return ""
	}
	if ownerType == ownerSite {
		return im.sites[entityID+"/"+code].id
	}
	return im.houses[entityID+"/"+code].id
}

// resolveReferenceKinds checks every reference the four kinds make against the package and the
// tenant's rows, and every rule the store applies when it creates them.
func (im *importer) resolveReferenceKinds(pkg OrgPackage, p packageProblems) error {
	if len(pkg.Units)+len(pkg.Conversions)+len(pkg.CurrencyRoundingRules)+len(pkg.NameTranslations) == 0 {
		return nil
	}
	if err := im.loadReferenceData(); err != nil {
		return err
	}
	im.resolveUnits(pkg, p)
	im.resolveRounding(pkg, p)
	return im.resolveTranslations(pkg, p)
}

func (im *importer) resolveUnits(pkg OrgPackage, p packageProblems) {
	inPackage := map[string]string{} // code -> class
	for _, u := range pkg.Units {
		inPackage[u.Code] = u.Class
	}
	for i, c := range pkg.Conversions {
		if _, exists := im.refConvs[conversionPair(c.FromCode, c.ToCode)]; exists {
			continue
		}
		classes := map[string]string{}
		for field, code := range map[string]string{"fromCode": c.FromCode, "toCode": c.ToCode} {
			path := indexPath(kindConversion, i) + "." + field
			if existing, ok := im.refUnits[code]; ok {
				classes[field] = existing.Class
				if existing.Status != statusActive {
					p.add(path, "unit "+code+" is inactive")
				}
			} else if class, ok := inPackage[code]; ok {
				classes[field] = class
			} else {
				p.add(path, "unknown unit "+code)
			}
		}
		if from, to := classes["fromCode"], classes["toCode"]; from != "" && to != "" && from != to {
			p.add(indexPath(kindConversion, i), fmt.Sprintf("%s is %s and %s is %s", c.FromCode, from, c.ToCode, to))
		}
	}
}

func (im *importer) resolveRounding(pkg OrgPackage, p packageProblems) {
	inPackage := map[string]bool{}
	for _, e := range pkg.LegalEntities {
		inPackage[strings.ToUpper(e.Code)] = true
	}
	for i, r := range pkg.CurrencyRoundingRules {
		path := indexPath(kindRounding, i)
		entity := strings.ToUpper(r.LegalEntity)
		leID := ""
		if entity != "" {
			if e, ok := im.entities[entity]; ok {
				leID = e.id
			} else if !inPackage[entity] {
				p.add(path+".legalEntity", "unknown legal entity "+r.LegalEntity)
				continue
			}
		}
		// A legal entity the package creates has no id yet, so it has no rule either.
		if _, exists := im.roundings[roundingKey(r.Currency, r.Purpose, leID)]; exists && (entity == "" || leID != "") {
			continue
		}
		currency, ok := im.currencies[r.Currency]
		switch {
		case !ok || currency.MinorUnits == nil:
			p.add(path+".currency", "unknown currency "+r.Currency)
		case currency.Status != statusActive:
			p.add(path+".currency", "currency "+r.Currency+" is inactive")
		default:
			unit, _ := decimal.NewFromString(r.Unit)
			if err := domain.ValidateUnit(unit, int32(*currency.MinorUnits)); err != nil {
				p.add(path+".unit", "must be a power of ten no finer than the currency minor unit, or 5 or 25 times one")
			}
		}
	}
}

func (im *importer) resolveTranslations(pkg OrgPackage, p packageProblems) error {
	inPackage := map[string]bool{}
	for _, e := range pkg.LegalEntities {
		inPackage[ownerLegalEntity+"/"+strings.ToUpper(e.Code)] = true
	}
	for _, u := range pkg.OperatingUnits {
		inPackage[ownerOperatingUnit+"/"+u.Type+"/"+strings.ToUpper(u.Code)] = true
	}
	for _, s := range pkg.Sites {
		entity := strings.ToUpper(s.LegalEntity)
		inPackage[ownerSite+"/"+entity+"/"+strings.ToUpper(s.Code)] = true
		for _, w := range s.Warehouses {
			inPackage[ownerWarehouse+"/"+entity+"/"+strings.ToUpper(w.Code)] = true
		}
	}
	var languages map[string]bool
	for i, t := range pkg.NameTranslations {
		path := indexPath(kindTranslation, i)
		owner, _ := normalizeOwner(t.OwnerType, t.Owner)
		id := im.ownerID(t.OwnerType, owner)
		if id == "" && !inPackage[t.OwnerType+"/"+owner] {
			p.add(path+".owner", fmt.Sprintf("unknown %s %s", t.OwnerType, t.Owner))
			continue
		}
		if _, exists := im.trans[translationKey(t.OwnerType, id, t.Language)]; exists {
			continue
		}
		if languages == nil {
			active, err := im.st.ListReference(im.ctx, store.KindLanguage, store.ReferenceFilter{Status: statusActive})
			if err != nil {
				return err
			}
			languages = map[string]bool{}
			for _, l := range active {
				languages[l.Code] = true
			}
		}
		if !languages[t.Language[:2]] {
			p.add(path+".language", "language "+t.Language[:2]+" is unknown or inactive in this tenant")
		}
	}
	return nil
}

// scopeOf narrows the import's context to one legal entity, the scope its rows are writable under.
func (im *importer) scopeOf(legalEntityID string) context.Context {
	if legalEntityID == "" {
		return im.ctx
	}
	return scopedTo(im.ctx, legalEntityID)
}

// ownerEntityID returns the id of the legal entity a translation owner belongs to; empty for an
// operating unit, which belongs to none.
func (im *importer) ownerEntityID(ownerType, owner string) string {
	switch ownerType {
	case ownerLegalEntity:
		return im.entities[owner].id
	case ownerOperatingUnit:
		return ""
	}
	entity, _, _ := strings.Cut(owner, "/")
	return im.entities[entity].id
}

func (im *importer) differ(kind, code, field string) {
	im.report.Differences = append(im.report.Differences, ImportDifference{Kind: kind, Code: code, Field: field})
}

func (im *importer) unitsOf(pkg OrgPackage) error {
	for _, u := range pkg.Units {
		name := strings.TrimSpace(u.Name)
		if existing, ok := im.refUnits[u.Code]; ok {
			if existing.Name != name {
				im.differ(kindUnit, u.Code, "name")
			}
			if existing.Class != u.Class {
				im.differ(kindUnit, u.Code, "class")
			}
			if existing.DecimalPrecision != *u.DecimalPrecision {
				im.differ(kindUnit, u.Code, "decimalPrecision")
			}
			im.tally(kindUnit, true)
			continue
		}
		in := store.Unit{Code: u.Code, Name: name, Class: u.Class, DecimalPrecision: *u.DecimalPrecision, Status: statusActive}
		if !im.dryRun {
			if _, err := im.st.CreateUnit(im.ctx, in, keyPrefix+"uom:"+u.Code); err != nil {
				return err
			}
		}
		im.refUnits[u.Code] = in
		im.tally(kindUnit, false)
	}
	return nil
}

func (im *importer) conversionsOf(pkg OrgPackage) error {
	for _, c := range pkg.Conversions {
		pair := conversionPair(c.FromCode, c.ToCode)
		factor, _ := parseConversionDecimal(c.Factor)
		offset, _ := conversionOffset(c)
		if existing, ok := im.refConvs[pair]; ok {
			if existing.FromCode == c.FromCode {
				if !existing.Factor.Equal(factor) {
					im.differ(kindConversion, pair, "factor")
				}
				if !existing.Offset.Equal(offset) {
					im.differ(kindConversion, pair, "offset")
				}
			}
			im.tally(kindConversion, true)
			continue
		}
		in := store.Conversion{FromCode: c.FromCode, ToCode: c.ToCode, Factor: factor, Offset: offset}
		if !im.dryRun {
			if _, _, err := im.st.CreateConversion(im.ctx, in, keyPrefix+"conv:"+pair); err != nil {
				return err
			}
		}
		im.refConvs[pair] = in
		im.tally(kindConversion, false)
	}
	return nil
}

func (im *importer) roundingOf(pkg OrgPackage) error {
	for _, r := range pkg.CurrencyRoundingRules {
		entity := strings.ToUpper(r.LegalEntity)
		leID := im.entities[entity].id
		code := r.Currency + "/" + r.Purpose
		if entity != "" {
			code += "/" + entity
		}
		unit, _ := decimal.NewFromString(r.Unit)
		if existing, ok := im.roundings[roundingKey(r.Currency, r.Purpose, leID)]; ok {
			if !existing.Unit.Equal(unit) {
				im.differ(kindRounding, code, "unit")
			}
			if existing.Method != r.Method {
				im.differ(kindRounding, code, "method")
			}
			im.tally(kindRounding, true)
			continue
		}
		in := store.CurrencyRounding{CurrencyCode: r.Currency, Purpose: r.Purpose, Unit: unit, Method: r.Method}
		if entity != "" {
			in.LegalEntityID = &leID
		}
		if !im.dryRun {
			if _, _, err := im.st.PutCurrencyRounding(im.scopeOf(leID), in); err != nil {
				return err
			}
		}
		im.roundings[roundingKey(r.Currency, r.Purpose, leID)] = in
		im.tally(kindRounding, false)
	}
	return nil
}

func (im *importer) translationsOf(pkg OrgPackage) error {
	for _, t := range pkg.NameTranslations {
		owner, _ := normalizeOwner(t.OwnerType, t.Owner)
		id := im.ownerID(t.OwnerType, owner)
		name := strings.TrimSpace(t.Name)
		code := t.OwnerType + "/" + owner + "/" + t.Language
		key := translationKey(t.OwnerType, id, t.Language)
		if existing, ok := im.trans[key]; ok {
			if existing.Name != name {
				im.differ(kindTranslation, code, "name")
			}
			im.tally(kindTranslation, true)
			continue
		}
		if !im.dryRun {
			if _, _, err := im.st.PutNameTranslation(im.scopeOf(im.ownerEntityID(t.OwnerType, owner)), t.OwnerType, id, t.Language, name); err != nil {
				return err
			}
		}
		im.trans[key] = store.NameTranslation{OwnerType: t.OwnerType, OwnerID: id, Language: t.Language, Name: name}
		im.tally(kindTranslation, false)
	}
	return nil
}
