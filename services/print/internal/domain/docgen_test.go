package domain

import (
	"bytes"
	"strings"
	"testing"
)

func memBlocks(blocks ...TextBlock) BlockSource {
	return func(code string) ([]TextBlock, error) {
		var out []TextBlock
		for _, b := range blocks {
			if b.Code == code {
				out = append(out, b)
			}
		}
		return out, nil
	}
}

func TestFillText_ReplacesPlaceholdersAndNamesMissingOnes(t *testing.T) {
	got, err := FillText("X", "Dear {{ counterpartyName }}, {{amountDue}} is due; {{amountDue}} again.", map[string]string{"counterpartyName": "Ironbridge", "amountDue": "12.00 USD"})
	if err != nil || got != "Dear Ironbridge, 12.00 USD is due; 12.00 USD again." {
		t.Fatalf("got %q, %v", got, err)
	}
	_, err = FillText("DUNNING", "{{b}} {{a}} {{a}}", map[string]string{})
	if e, ok := err.(*Error); !ok || e.Code != CodeTextBlockData || !strings.Contains(e.Detail, "a, b") {
		t.Fatalf("err = %v", err)
	}
	if got := Placeholders("{{b}} {{a}} {{a}} {x}"); len(got) != 2 || got[0] != "a" {
		t.Fatalf("placeholders = %v", got)
	}
}

func TestPickBlock_ExactThenLanguageThenEnglish(t *testing.T) {
	vs := []TextBlock{{Language: "en", Body: "en"}, {Language: "de", Body: "de"}, {Language: "de-CH", Body: "de-CH"}}
	for lang, want := range map[string]string{"de-CH": "de-CH", "de-AT": "de", "fr": "en", "": "en"} {
		if b, ok := PickBlock(vs, lang); !ok || b.Body != want {
			t.Errorf("%q: got %q %v, want %q", lang, b.Body, ok, want)
		}
	}
	if _, ok := PickBlock([]TextBlock{{Language: "de"}}, "fr"); ok {
		t.Error("a block without English must not fall back to another language")
	}
}

func TestTemplateAndBlockValidation(t *testing.T) {
	ok := DocumentTemplate{Code: "INV-STD", Name: "Standard", DocumentType: DocCustomerInvoice, Config: TemplateConfig{NoteBlocks: []string{"TERMS"}}}
	if f := ok.Validate(); len(f) != 0 {
		t.Fatalf("valid template: %v", f)
	}
	bad := DocumentTemplate{Code: "inv", DocumentType: "INVOICE", Config: TemplateConfig{BodyBlock: "BODY"}}
	f := bad.Validate()
	for _, k := range []string{"code", "name", "documentType", "config.bodyBlock"} {
		if f[k] == "" {
			t.Errorf("no error for %s: %v", k, f)
		}
	}
	if f := (TextBlock{Code: "TERMS", Language: "english", Body: " "}).Validate(); f["language"] == "" || f["body"] == "" {
		t.Errorf("block errors: %v", f)
	}
}

func TestCompose_AppliesTemplateColumnsFootersAndFormNotes(t *testing.T) {
	tpl := &DocumentTemplate{Code: "INV-STD", DocumentType: DocCustomerInvoice, Config: TemplateConfig{
		Title: "Tax invoice", FooterText: "Cedar Ridge Pumps, Inc. - Registered in Ohio",
		RenameColumns: map[string]string{"Qty": "Quantity"}, HideColumns: []string{"Unit"}, NoteBlocks: []string{"TERMS"},
	}}
	req := DocumentRequest{DocumentType: DocCustomerInvoice, Number: "INV-1", LegalEntity: DocParty{Name: "Cedar Ridge Pumps"},
		Columns: []DocColumn{{Header: "Item"}, {Header: "Unit"}, {Header: "Qty", Align: "R"}},
		Lines:   [][]string{{"Casting", "ea", "4"}}, Notes: "Thanks."}
	got, err := Compose(req, tpl, ComposeInput{Language: "de", Values: map[string]string{"dueDate": "2026-12-01"}},
		memBlocks(TextBlock{Code: "TERMS", Language: "en", Body: "Due {{dueDate}} for {{documentNumber}}."}, TextBlock{Code: "TERMS", Language: "de", Body: "Fällig am {{dueDate}} für {{documentNumber}}."}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Tax invoice" || len(got.Columns) != 2 || got.Columns[1].Header != "Quantity" || got.Columns[1].Align != "R" ||
		len(got.Lines[0]) != 2 || got.Lines[0][1] != "4" {
		t.Fatalf("columns/title: %+v", got)
	}
	if got.Notes != "Thanks.\n\nFällig am 2026-12-01 für INV-1." {
		t.Fatalf("notes = %q", got.Notes)
	}
	if len(req.Columns) != 3 || req.Notes != "Thanks." {
		t.Fatal("Compose changed its input")
	}
	if _, err := Compose(req, &DocumentTemplate{Code: "X", DocumentType: DocPurchaseOrder}, ComposeInput{}, memBlocks()); err == nil {
		t.Error("a template of another document type was applied")
	}
	if _, err := Compose(req, tpl, ComposeInput{}, memBlocks()); err == nil || !strings.Contains(err.Error(), "TERMS") {
		t.Errorf("missing block error = %v", err)
	}
}

func statementLetter() DocumentRequest {
	return DocumentRequest{
		DocumentType: DocPeriodicStatement, Number: "ST-2026-10",
		LegalEntity:  DocParty{Name: "Cedar Ridge Pumps, Inc.", Address: []string{"400 Industrial Way", "Dayton, OH 45402"}},
		Counterparty: &DocParty{Name: "Contoso Ltd", Address: []string{"1 Contoso Plaza", "Columbus, OH 43004"}},
		Fields:       []DocField{{Label: "Statement date", Value: "2026-10-31"}, {Label: "Currency", Value: "USD"}},
		Body:         "Dear Contoso Ltd,\n\nthis statement lists the open items on your account up to 2026-10-31. Please settle the balance of 1,250.00 USD by 2026-11-30.",
		Columns:      []DocColumn{{Header: "Date"}, {Header: "Document"}, {Header: "Debit", Align: "R"}, {Header: "Credit", Align: "R"}, {Header: "Balance", Align: "R"}},
		Lines: [][]string{
			{"2026-10-02", "INV-1001", "900.00", "", "900.00"},
			{"2026-10-15", "INV-1007", "600.00", "", "1,500.00"},
			{"2026-10-20", "PMT-5521", "", "250.00", "1,250.00"},
		},
		Totals: []DocField{{Label: "Balance due USD", Value: "1,250.00"}},
	}
}

func balanceConfirmation() DocumentRequest {
	return DocumentRequest{
		DocumentType: DocBalanceConfirmation, Number: "BC-2026-12", FooterText: "Please sign and return one copy.",
		LegalEntity:  DocParty{Name: "Cedar Ridge Pumps, Inc.", Address: []string{"400 Industrial Way", "Dayton, OH 45402"}},
		Counterparty: &DocParty{Name: "Ironbridge Foundry", Address: []string{"88 Foundry Rd"}},
		Body:         "As of 2026-12-31 our records show a balance of 5,676.00 USD in our favour on your account. If this differs from yours, tell us within 14 days.",
	}
}

func TestRenderDocument_CorrespondenceMatchesGolden(t *testing.T) {
	templated := sampleDocument(DocCustomerInvoice)
	templated.Title = "Tax invoice"
	templated.FooterText = "Cedar Ridge Pumps, Inc. - Registered in Ohio"
	templated.Notes = "Payment is due within 30 days of the invoice date."
	cases := map[string]DocumentRequest{
		"statement-of-account.pdf":  statementLetter(),
		"balance-confirmation.pdf":  balanceConfirmation(),
		"invoice-with-template.pdf": templated,
	}
	for name, d := range cases {
		first, err := RenderDocument(d)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		again, _ := RenderDocument(d)
		if !bytes.Equal(first, again) {
			t.Fatalf("%s: two renders differ", name)
		}
		checkGolden(t, name, first)
	}
}

func TestRenderDocument_CorrespondenceNeedsABodyAndOthersRefuseOne(t *testing.T) {
	d := balanceConfirmation()
	d.Body = ""
	if _, err := RenderDocument(d); err == nil || !strings.Contains(err.Error(), "body is required") {
		t.Fatalf("letter without body: %v", err)
	}
	inv := sampleDocument(DocCustomerInvoice)
	inv.Body = "x"
	if _, err := RenderDocument(inv); err == nil {
		t.Fatal("an invoice accepted a letter body")
	}
	inv = sampleDocument(DocCustomerInvoice)
	inv.Columns, inv.Lines = nil, nil
	if _, err := RenderDocument(inv); err == nil {
		t.Fatal("an invoice without columns rendered")
	}
}

func TestRenderDocument_TitleAndFooterAreDrawn(t *testing.T) {
	d := sampleDocument(DocCustomerInvoice)
	d.Title, d.FooterText = "Tax invoice", "Registered in Ohio"
	pdf, err := RenderDocument(d)
	if err != nil {
		t.Fatal(err)
	}
	if !shows(pdf, "Tax invoice") || !shows(pdf, "Registered in Ohio") || shows(pdf, "Invoice") {
		t.Fatal("title or footer text missing, or the standard title is still drawn")
	}
}

func TestResolveOutputs_MostSpecificScopePerCopyKindAndChannel(t *testing.T) {
	rules := []OutputRule{
		{ID: "1", DocumentType: DocCustomerInvoice, CopyKind: CopyOriginal, Channel: ChannelPrinter, Destination: "OFFICE", Copies: 1, Sequence: 1},
		{ID: "2", DocumentType: DocCustomerInvoice, CopyKind: CopyCopy, Channel: ChannelArchive, Copies: 1, Sequence: 1},
		{ID: "3", DocumentType: DocCustomerInvoice, AccountID: "cust-1", CopyKind: CopyOriginal, Channel: ChannelEmail, Destination: "ap@contoso.example", Copies: 1, Sequence: 1},
		{ID: "4", DocumentType: DocCustomerInvoice, LegalEntityID: "le-1", CopyKind: CopyOriginal, Channel: ChannelPrinter, Destination: "DOCK", Copies: 2, Sequence: 1},
		{ID: "5", DocumentType: DocCustomerInvoice, LegalEntityID: "le-1", AccountID: "cust-1", CopyKind: CopyOriginal, Channel: ChannelPrinter, Destination: "VIP", Copies: 1, Sequence: 2},
		{ID: "6", DocumentType: DocPurchaseOrder, CopyKind: CopyOriginal, Channel: ChannelPrinter, Destination: "OFFICE", Copies: 1, Sequence: 1},
		{ID: "7", DocumentType: DocCustomerInvoice, LegalEntityID: "le-2", CopyKind: CopyOriginal, Channel: ChannelPrinter, Destination: "OTHER", Copies: 1, Sequence: 1},
	}
	ids := func(d OutputDocument) string {
		var s []string
		for _, r := range ResolveOutputs(rules, d) {
			s = append(s, r.ID)
		}
		return strings.Join(s, ",")
	}
	// Entity and account: the printer rule of that pair wins over the entity's and the default; the account's e-mail is its own channel.
	if got := ids(OutputDocument{Type: DocCustomerInvoice, LegalEntityID: "le-1", AccountID: "cust-1"}); got != "5,3,2" {
		t.Errorf("entity+account = %s", got)
	}
	if got := ids(OutputDocument{Type: DocCustomerInvoice, LegalEntityID: "le-1", AccountID: "cust-9"}); got != "4,2" {
		t.Errorf("entity only = %s", got)
	}
	if got := ids(OutputDocument{Type: DocCustomerInvoice, LegalEntityID: "le-3", AccountID: "cust-9"}); got != "1,2" {
		t.Errorf("default = %s", got)
	}
	if got := ids(OutputDocument{Type: DocPackingSlip}); got != "" {
		t.Errorf("no rule = %s", got)
	}
}

func TestOutputRuleValidation(t *testing.T) {
	good := []OutputRule{
		{DocumentType: DocCustomerInvoice, CopyKind: CopyOriginal, Channel: ChannelPrinter, Destination: "OFFICE", Copies: 2, Sequence: 1},
		{DocumentType: DocCustomerInvoice, CopyKind: CopyCopy, Channel: ChannelEmail, Destination: "ap@contoso.example", Copies: 1, Sequence: 1},
		{DocumentType: DocCustomerInvoice, CopyKind: CopyCopy, Channel: ChannelArchive, Copies: 1, Sequence: 1},
	}
	for _, r := range good {
		if f := r.Validate(); len(f) != 0 {
			t.Errorf("%+v: %v", r, f)
		}
	}
	bad := []OutputRule{
		{DocumentType: "INVOICE", CopyKind: CopyOriginal, Channel: ChannelArchive, Copies: 1, Sequence: 1},
		{DocumentType: DocCustomerInvoice, CopyKind: "DUPLICATE", Channel: ChannelArchive, Copies: 1, Sequence: 1},
		{DocumentType: DocCustomerInvoice, CopyKind: CopyCopy, Channel: ChannelEmail, Destination: "a@b.example, c@d.example", Copies: 1, Sequence: 1},
		{DocumentType: DocCustomerInvoice, CopyKind: CopyCopy, Channel: ChannelArchive, Destination: "x", Copies: 1, Sequence: 1},
		{DocumentType: DocCustomerInvoice, CopyKind: CopyCopy, Channel: ChannelPortal, Copies: 3, Sequence: 1},
		{DocumentType: DocCustomerInvoice, CopyKind: CopyCopy, Channel: ChannelPrinter, Destination: "office", Copies: 1, Sequence: 0},
	}
	for _, r := range bad {
		if f := r.Validate(); len(f) == 0 {
			t.Errorf("%+v was accepted", r)
		}
	}
}
