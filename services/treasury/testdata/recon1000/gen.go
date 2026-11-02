//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type item struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	ValueDate    string `json:"valueDate"`
	Reference    string `json:"reference"`
	Counterparty string `json:"counterparty"`
	Batch        string `json:"batch"`
}

type line struct {
	cents    int64
	day      int
	ref      string
	cp       string
	items    []string
	group    string
	notProvd bool
}

func money(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

func swift(c int64) string { return strings.Replace(money(c), ".", ",", 1) }

func date(day int) string { return fmt.Sprintf("2026-10-%02d", day) }

func yymmdd(day int) string { return fmt.Sprintf("2610%02d", day) }

func main() {
	var items []item
	var camt, bai, mt []line
	add := func(id string, c int64, day int, ref, cp, batch string) {
		items = append(items, item{Type: "ar", ID: id, Amount: money(c), Currency: "EUR", ValueDate: date(day), Reference: ref, Counterparty: cp, Batch: batch})
	}
	for i := 0; i < 850; i++ {
		id, c, day := fmt.Sprintf("tx-%d", i), int64(10000+(i%53)*100), 1+i%28
		ref, cp := fmt.Sprintf("E2E%05d", i), fmt.Sprintf("Vendor %03d Holdings", i)
		add(id, c, day, ref, cp, "")
		switch {
		case i < 300:
			camt = append(camt, line{c, day, ref, cp, []string{id}, "exact", false})
		case i < 450:
			bai = append(bai, line{c, day, ref, "", []string{id}, "exact", false})
		case i < 620:
			mt = append(mt, line{c, day, ref, cp, []string{id}, "exact", false})
		case i < 720:
			camt = append(camt, line{c, day + 1, "", cp, []string{id}, "payer", true})
		case i < 820:
			mt = append(mt, line{c, day + 1, "", cp, []string{id}, "payer", false})
		case i < 835:
			camt = append(camt, line{c, day + 1, ref, fmt.Sprintf("ZQX%d unknown payer", i), []string{id}, "reference-only", true})
		default:
			mt = append(mt, line{c, day + 1, ref, fmt.Sprintf("ZQX%d unknown payer", i), []string{id}, "reference-only", false})
		}
	}
	for b := 0; b < 100; b++ {
		batch := fmt.Sprintf("RMT%06d", b)
		var total int64
		var ids []string
		for k := 0; k < 2+b%3; k++ {
			id, c := fmt.Sprintf("inv-%d-%d", b, k), int64(5000+b*37+k*211)
			add(id, c, 10+b%10, fmt.Sprintf("INV%03d%02d", b, k), "Remitter "+batch, batch)
			total += c
			ids = append(ids, id)
		}
		bai = append(bai, line{total, 11 + b%10, batch, "", ids, "batch", false})
	}
	for i := 0; i < 50; i++ {
		id, day, ref := fmt.Sprintf("fee-%d", i), 5+i%20, fmt.Sprintf("FEE-%04d", i)
		add(id, -1250, day, ref, "Example Bank AG", "")
		camt = append(camt, line{-1250, day, ref, "Example Bank AG", []string{id}, "fee", true})
	}
	for i := 0; i < 100; i++ {
		c, day := int64(10000+(i%53)*100), 1+i%28
		add(fmt.Sprintf("decoy-vendor-%d", i), c, min(day+3, 31), fmt.Sprintf("E2E9%04d", i), fmt.Sprintf("Vendor %03d Holdings", i), "")
		add(fmt.Sprintf("decoy-same-day-%d", i), c, day, fmt.Sprintf("E2E8%04d", i), fmt.Sprintf("Vendor %03d Holdings", i), "")
		add(fmt.Sprintf("decoy-unrelated-%d", i), int64(333333+i*17), 1+i%28, fmt.Sprintf("ZZ%06d", i), fmt.Sprintf("Unrelated %d Ltd", i), "")
	}

	dir := filepath.Dir(os.Args[0])
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			panic(err)
		}
	}
	sum := func(ls []line) int64 {
		var t int64
		for _, l := range ls {
			t += l.cents
		}
		return t
	}
	expected := map[string]map[string][][]string{"matched": {}, "suggested": {}}
	for name, ls := range map[string][]line{"camt053.xml": camt, "bai2.txt": bai, "mt940.txt": mt} {
		for _, l := range ls {
			kind := "matched"
			if l.group == "reference-only" {
				kind = "suggested"
			}
			expected[kind][name] = append(expected[kind][name], l.items)
		}
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"><BkToCstmrStmt><Stmt><Id>REC1000-CAMT</Id>`)
	b.WriteString(`<Acct><Id><IBAN>DE89370400440532013000</IBAN></Id><Ccy>EUR</Ccy></Acct>`)
	b.WriteString(`<Bal><Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">1000.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-01</Dt></Dt></Bal>`)
	fmt.Fprintf(&b, `<Bal><Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">%s</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-31</Dt></Dt></Bal>`, money(100000+sum(camt)))
	for _, l := range camt {
		ind, c := "CRDT", l.cents
		party := fmt.Sprintf("<Dbtr><Nm>%s</Nm></Dbtr>", l.cp)
		if c < 0 {
			ind, c, party = "DBIT", -c, fmt.Sprintf("<Cdtr><Nm>%s</Nm></Cdtr>", l.cp)
		}
		e2e, remit := l.ref, ""
		switch {
		case l.ref == "":
			e2e = "NOTPROVIDED"
		case l.notProvd:
			e2e, remit = "NOTPROVIDED", fmt.Sprintf("<RmtInf><Ustrd>%s</Ustrd></RmtInf>", l.ref)
		}
		fmt.Fprintf(&b, `<Ntry><Amt Ccy="EUR">%s</Amt><CdtDbtInd>%s</CdtDbtInd><BookgDt><Dt>%s</Dt></BookgDt><ValDt><Dt>%s</Dt></ValDt><NtryDtls><TxDtls><Refs><EndToEndId>%s</EndToEndId></Refs>%s<RltdPties>%s</RltdPties></TxDtls></NtryDtls></Ntry>`,
			money(c), ind, date(l.day), date(l.day), e2e, remit, party)
	}
	b.WriteString(`</Stmt></BkToCstmrStmt></Document>`)
	write("camt053.xml", b.String())

	b.Reset()
	b.WriteString("01,SENDERBANK,RECEIVERCO,261031,1200,1,,,2/\n02,RECEIVERCO,SENDERBANK,1,261031,1200,EUR,2/\n")
	fmt.Fprintf(&b, "03,DE89370400440532013000,EUR,010,100000,,,015,%d,,/\n", 100000+sum(bai))
	for i, l := range bai {
		fmt.Fprintf(&b, "16,165,%d,V,%s,,BR%06d,%s,%s/\n", l.cents, yymmdd(l.day), i, l.ref, l.ref)
	}
	fmt.Fprintf(&b, "49,0,%d/\n98,0,1,%d/\n99,0,1,%d/\n", len(bai)+3, len(bai)+4, len(bai)+5)
	write("bai2.txt", b.String())

	b.Reset()
	b.WriteString("{1:F01BANKDEFFAXXX0000000000}{2:O9400000000000BANKDEFFAXXX00000000000000000000N}{4:\n:20:REC1000MT\n:25:DE89370400440532013000\n:28C:00001/001\n:60F:C261001EUR1000,00\n")
	for i, l := range mt {
		fmt.Fprintf(&b, ":61:%s%02d%02dC%sNTRFNONREF//BR%06d\n:86:166?00GUTSCHRIFT?20%s?32%s\n", yymmdd(l.day), 10, l.day, swift(l.cents), i, l.ref, l.cp)
	}
	fmt.Fprintf(&b, ":62F:C261031EUR%s\n-}\n", swift(100000+sum(mt)))
	write("mt940.txt", b.String())

	raw, _ := json.MarshalIndent(items, "", " ")
	write("items.json", string(raw)+"\n")
	raw, _ = json.MarshalIndent(expected, "", " ")
	write("expected.json", string(raw)+"\n")
}
