package domain

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"

	"github.com/shopspring/decimal"
)

// MX_CFDI40: an income CFDI 4.0 before stamping, checked against the
// structure and patterns of SAT's cfdv40.xsd and tdCFDI.xsd (required
// attributes, Version 4.0, t_FechaH, t_RFC, t_Importe, 20-digit certificate
// number, five-digit postal codes) and the Anexo 20 arithmetic: Importe =
// Cantidad x ValorUnitario, transfer tax = Base x TasaOCuota, SubTotal = sum
// of Importe, the tax summary per (Impuesto, TipoFactor, TasaOCuota) equal to
// the lines, and Total = SubTotal - Descuento + transferred - withheld taxes.
// The SAT catalogues (catCFDI.xsd, about 6 MB) are not embedded: catalogue
// codes are checked for shape only. TestMXFixtureAgainstSATXSD validates the
// fixture against the real SAT schemas when they are available locally.

const cfdiNS = "http://www.sat.gob.mx/cfd/4"

var (
	cfdiFecha   = regexp.MustCompile(`^(20[1-9][0-9])-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T(([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9])$`)
	cfdiRFC     = regexp.MustCompile(`^[A-Z&Ñ]{3,4}[0-9]{2}(0[1-9]|1[012])(0[1-9]|[12][0-9]|3[01])[A-Z0-9]{2}[0-9A]$`)
	cfdiImporte = regexp.MustCompile(`^[0-9]{1,18}(\.[0-9]{1,6})?$`)
	cfdiCP      = regexp.MustCompile(`^[0-9]{5}$`)
	cfdiNoCert  = regexp.MustCompile(`^[0-9]{20}$`)
	cfdiText    = regexp.MustCompile(`^[^|]{1,1000}$`)
	cfdiName    = regexp.MustCompile(`^[^|]{1,300}$`)
	cfdiProd    = regexp.MustCompile(`^[0-9]{8}$`)
	cfdiCode3   = regexp.MustCompile(`^[0-9]{3}$`)
	cfdiCode2   = regexp.MustCompile(`^[0-9]{2}$`)
	cfdiMoneda  = regexp.MustCompile(`^[A-Z]{3}$`)
	cfdiUnidad  = regexp.MustCompile(`^[0-9A-Z]{1,3}$`)
	cfdiExport  = regexp.MustCompile(`^0[1-4]$`)
	cfdiTasa    = regexp.MustCompile(`^[0-9]\.[0-9]{6}$`)
	cfdiUso     = regexp.MustCompile(`^(G0[1-3]|I0[1-8]|D(0[1-9]|10)|S01|CP01|CN01)$`)
)

var cfdiCent = decimal.RequireFromString("0.01")

type cfdiTraslado struct {
	Base       string `xml:"Base,attr"`
	Impuesto   string `xml:"Impuesto,attr"`
	TipoFactor string `xml:"TipoFactor,attr"`
	TasaOCuota string `xml:"TasaOCuota,attr"`
	Importe    string `xml:"Importe,attr"`
}

type cfdiImpuestos struct {
	TotalTrasladados string `xml:"TotalImpuestosTrasladados,attr"`
	TotalRetenidos   string `xml:"TotalImpuestosRetenidos,attr"`
	Traslados        struct {
		Traslado []cfdiTraslado `xml:"http://www.sat.gob.mx/cfd/4 Traslado"`
	} `xml:"http://www.sat.gob.mx/cfd/4 Traslados"`
	Retenciones struct {
		Retencion []struct {
			Importe string `xml:"Importe,attr"`
		} `xml:"http://www.sat.gob.mx/cfd/4 Retencion"`
	} `xml:"http://www.sat.gob.mx/cfd/4 Retenciones"`
}

type cfdiConcepto struct {
	ClaveProdServ string         `xml:"ClaveProdServ,attr"`
	Cantidad      string         `xml:"Cantidad,attr"`
	ClaveUnidad   string         `xml:"ClaveUnidad,attr"`
	Descripcion   string         `xml:"Descripcion,attr"`
	ValorUnitario string         `xml:"ValorUnitario,attr"`
	Importe       string         `xml:"Importe,attr"`
	Descuento     string         `xml:"Descuento,attr"`
	ObjetoImp     string         `xml:"ObjetoImp,attr"`
	Impuestos     *cfdiImpuestos `xml:"http://www.sat.gob.mx/cfd/4 Impuestos"`
}

type cfdiDoc struct {
	XMLName           xml.Name `xml:"http://www.sat.gob.mx/cfd/4 Comprobante"`
	Version           string   `xml:"Version,attr"`
	Fecha             string   `xml:"Fecha,attr"`
	Sello             string   `xml:"Sello,attr"`
	NoCertificado     string   `xml:"NoCertificado,attr"`
	Certificado       string   `xml:"Certificado,attr"`
	MetodoPago        string   `xml:"MetodoPago,attr"`
	SubTotal          string   `xml:"SubTotal,attr"`
	Descuento         string   `xml:"Descuento,attr"`
	Moneda            string   `xml:"Moneda,attr"`
	Total             string   `xml:"Total,attr"`
	TipoDeComprobante string   `xml:"TipoDeComprobante,attr"`
	Exportacion       string   `xml:"Exportacion,attr"`
	LugarExpedicion   string   `xml:"LugarExpedicion,attr"`
	Emisor            *struct {
		Rfc           string `xml:"Rfc,attr"`
		Nombre        string `xml:"Nombre,attr"`
		RegimenFiscal string `xml:"RegimenFiscal,attr"`
	} `xml:"http://www.sat.gob.mx/cfd/4 Emisor"`
	Receptor *struct {
		Rfc                     string `xml:"Rfc,attr"`
		Nombre                  string `xml:"Nombre,attr"`
		DomicilioFiscalReceptor string `xml:"DomicilioFiscalReceptor,attr"`
		RegimenFiscalReceptor   string `xml:"RegimenFiscalReceptor,attr"`
		UsoCFDI                 string `xml:"UsoCFDI,attr"`
	} `xml:"http://www.sat.gob.mx/cfd/4 Receptor"`
	Conceptos struct {
		Concepto []cfdiConcepto `xml:"http://www.sat.gob.mx/cfd/4 Concepto"`
	} `xml:"http://www.sat.gob.mx/cfd/4 Conceptos"`
	Impuestos *cfdiImpuestos `xml:"http://www.sat.gob.mx/cfd/4 Impuestos"`
}

// ValidateCFDI40 checks a CFDI 4.0 against the rules above; it returns every failure.
func ValidateCFDI40(raw []byte) []FieldError {
	var errs fieldErrs
	var c cfdiDoc
	if err := xml.NewDecoder(bytes.NewReader(raw)).Decode(&c); err != nil {
		errs.add("xml", "%s (root must be cfdi:Comprobante in %s)", err, cfdiNS)
		return errs
	}
	match := func(field, v string, re *regexp.Regexp, what string) {
		if !re.MatchString(v) {
			errs.add(field, "%q must be %s", v, what)
		}
	}
	if c.Version != "4.0" {
		errs.add("Version", "must be 4.0")
	}
	match("Fecha", c.Fecha, cfdiFecha, "a t_FechaH date-time")
	match("NoCertificado", c.NoCertificado, cfdiNoCert, "20 digits")
	if c.Sello == "" || c.Certificado == "" {
		errs.add("Sello", "Sello and Certificado are required")
	}
	match("Moneda", c.Moneda, cfdiMoneda, "a c_Moneda code")
	match("LugarExpedicion", c.LugarExpedicion, cfdiCP, "a five-digit postal code")
	match("Exportacion", c.Exportacion, cfdiExport, "a c_Exportacion code (01 to 04)")
	if c.TipoDeComprobante != "I" {
		errs.add("TipoDeComprobante", "%q: this validator covers income (I) CFDI only", c.TipoDeComprobante)
	}
	if c.MetodoPago != "" && c.MetodoPago != "PUE" && c.MetodoPago != "PPD" {
		errs.add("MetodoPago", "must be PUE or PPD")
	}
	if e := c.Emisor; e == nil {
		errs.add("Emisor", "is required")
	} else {
		match("Emisor/Rfc", e.Rfc, cfdiRFC, "a t_RFC")
		match("Emisor/Nombre", e.Nombre, cfdiName, "1 to 300 characters without '|'")
		match("Emisor/RegimenFiscal", e.RegimenFiscal, cfdiCode3, "a c_RegimenFiscal code")
	}
	if r := c.Receptor; r == nil {
		errs.add("Receptor", "is required")
	} else {
		match("Receptor/Rfc", r.Rfc, cfdiRFC, "a t_RFC")
		match("Receptor/Nombre", r.Nombre, cfdiName, "1 to 300 characters without '|'")
		match("Receptor/DomicilioFiscalReceptor", r.DomicilioFiscalReceptor, cfdiCP, "a five-digit postal code")
		match("Receptor/RegimenFiscalReceptor", r.RegimenFiscalReceptor, cfdiCode3, "a c_RegimenFiscal code")
		match("Receptor/UsoCFDI", r.UsoCFDI, cfdiUso, "a c_UsoCFDI code")
	}
	amount := func(field, v string) decimal.Decimal {
		if !cfdiImporte.MatchString(v) {
			errs.add(field, "%q must be a t_Importe (up to 18 digits, 6 decimals, not negative)", v)
			return decimal.Zero
		}
		return decimal.RequireFromString(v)
	}
	money := func(field, v string) decimal.Decimal { // MXN amounts carry at most two decimals
		d := amount(field, v)
		if dot := strings.IndexByte(v, '.'); c.Moneda == "MXN" && dot >= 0 && len(v)-dot-1 > 2 {
			errs.add(field, "an MXN amount has at most two decimals")
		}
		return d
	}
	if len(c.Conceptos.Concepto) == 0 {
		errs.add("Conceptos", "at least one Concepto is required")
	}
	type taxKey struct{ impuesto, factor, tasa string }
	lineTax := map[taxKey][2]decimal.Decimal{}
	subtotal, lineDiscounts := decimal.Zero, decimal.Zero
	for i, k := range c.Conceptos.Concepto {
		f := fmt.Sprintf("Concepto[%d]", i)
		match(f+"/ClaveProdServ", k.ClaveProdServ, cfdiProd, "an eight-digit c_ClaveProdServ")
		match(f+"/ClaveUnidad", k.ClaveUnidad, cfdiUnidad, "a c_ClaveUnidad code")
		match(f+"/Descripcion", k.Descripcion, cfdiText, "1 to 1000 characters without '|'")
		match(f+"/ObjetoImp", k.ObjetoImp, cfdiCode2, "a c_ObjetoImp code")
		qty := amount(f+"/Cantidad", k.Cantidad)
		if !qty.IsPositive() {
			errs.add(f+"/Cantidad", "must be at least 0.000001")
		}
		importe := money(f+"/Importe", k.Importe)
		if want := qty.Mul(amount(f+"/ValorUnitario", k.ValorUnitario)); importe.Sub(want).Abs().GreaterThanOrEqual(cfdiCent) {
			errs.add(f+"/Importe", "%s is not Cantidad x ValorUnitario (%s)", importe, want)
		}
		subtotal = subtotal.Add(importe)
		if k.Descuento != "" {
			lineDiscounts = lineDiscounts.Add(money(f+"/Descuento", k.Descuento))
		}
		switch {
		case k.ObjetoImp == "02" && (k.Impuestos == nil || len(k.Impuestos.Traslados.Traslado) == 0 && len(k.Impuestos.Retenciones.Retencion) == 0):
			errs.add(f+"/Impuestos", "ObjetoImp 02 (subject to tax) needs the line taxes")
		case k.ObjetoImp == "01" && k.Impuestos != nil:
			errs.add(f+"/Impuestos", "ObjetoImp 01 (not subject to tax) carries no taxes")
		}
		if k.Impuestos == nil {
			continue
		}
		for ti, t := range k.Impuestos.Traslados.Traslado {
			tf := fmt.Sprintf("%s/Traslado[%d]", f, ti)
			base, imp := cfdiTrasladoChecks(&errs, tf, t, amount)
			key := taxKey{t.Impuesto, t.TipoFactor, t.TasaOCuota}
			sum := lineTax[key]
			lineTax[key] = [2]decimal.Decimal{sum[0].Add(base), sum[1].Add(imp)}
		}
	}
	if st := money("SubTotal", c.SubTotal); !st.Equal(subtotal) {
		errs.add("SubTotal", "%s is not the sum of the Concepto Importe (%s)", st, subtotal.StringFixed(2))
	}
	discount := decimal.Zero
	if c.Descuento != "" {
		discount = money("Descuento", c.Descuento)
	}
	if !discount.Equal(lineDiscounts) {
		errs.add("Descuento", "%s is not the sum of the line discounts (%s)", discount, lineDiscounts)
	}
	transferred, withheld := decimal.Zero, decimal.Zero
	if im := c.Impuestos; im != nil {
		summary := map[taxKey][2]decimal.Decimal{}
		for ti, t := range im.Traslados.Traslado {
			base, imp := cfdiTrasladoChecks(&errs, fmt.Sprintf("Impuestos/Traslado[%d]", ti), t, amount)
			summary[taxKey{t.Impuesto, t.TipoFactor, t.TasaOCuota}] = [2]decimal.Decimal{base, imp}
			transferred = transferred.Add(imp)
		}
		for k, v := range lineTax {
			if s, ok := summary[k]; !ok || !s[0].Equal(v[0].Round(2)) || !s[1].Equal(v[1].Round(2)) {
				errs.add("Impuestos/Traslados", "the summary for %s %s %s must equal the lines: Base %s, Importe %s", k.impuesto, k.factor, k.tasa, v[0].StringFixed(2), v[1].StringFixed(2))
			}
		}
		if len(summary) != len(lineTax) {
			errs.add("Impuestos/Traslados", "the summary has %d tax groups, the lines %d", len(summary), len(lineTax))
		}
		if tt := money("Impuestos/TotalImpuestosTrasladados", im.TotalTrasladados); !tt.Equal(transferred) {
			errs.add("Impuestos/TotalImpuestosTrasladados", "%s is not the sum of the transferred taxes (%s)", tt, transferred)
		}
		for ri, r := range im.Retenciones.Retencion {
			withheld = withheld.Add(money(fmt.Sprintf("Impuestos/Retencion[%d]", ri), r.Importe))
		}
		if im.TotalRetenidos != "" {
			if tr := money("Impuestos/TotalImpuestosRetenidos", im.TotalRetenidos); !tr.Equal(withheld) {
				errs.add("Impuestos/TotalImpuestosRetenidos", "%s is not the sum of the withheld taxes (%s)", tr, withheld)
			}
		}
	} else if len(lineTax) > 0 {
		errs.add("Impuestos", "the tax summary is required when lines carry taxes")
	}
	want := subtotal.Sub(discount).Add(transferred).Sub(withheld)
	if total := money("Total", c.Total); !total.Equal(want) {
		errs.add("Total", "%s is not SubTotal - Descuento + transferred - withheld (%s)", total, want.StringFixed(2))
	}
	return errs
}

func cfdiTrasladoChecks(errs *fieldErrs, f string, t cfdiTraslado, amount func(string, string) decimal.Decimal) (base, imp decimal.Decimal) {
	base = amount(f+"/Base", t.Base)
	if t.Impuesto != "001" && t.Impuesto != "002" && t.Impuesto != "003" {
		errs.add(f+"/Impuesto", "%q must be 001 (ISR), 002 (IVA) or 003 (IEPS)", t.Impuesto)
	}
	switch t.TipoFactor {
	case "Exento":
		if t.TasaOCuota != "" || t.Importe != "" {
			errs.add(f, "an Exento transfer carries no TasaOCuota or Importe")
		}
		return base, decimal.Zero
	case "Tasa", "Cuota":
	default:
		errs.add(f+"/TipoFactor", "%q must be Tasa, Cuota or Exento", t.TipoFactor)
		return base, decimal.Zero
	}
	if !cfdiTasa.MatchString(t.TasaOCuota) {
		errs.add(f+"/TasaOCuota", "%q must carry six decimals", t.TasaOCuota)
		return base, decimal.Zero
	}
	imp = amount(f+"/Importe", t.Importe)
	if want := base.Mul(decimal.RequireFromString(t.TasaOCuota)); imp.Sub(want).Abs().GreaterThanOrEqual(cfdiCent) {
		errs.add(f+"/Importe", "%s is not Base x TasaOCuota (%s)", imp, want)
	}
	return base, imp
}
