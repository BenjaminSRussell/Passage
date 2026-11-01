package domain

import (
	"strings"
	"testing"
	"time"
)

const attTenant = "00000000-0000-4000-8000-000000000001"

func validFile() Attachment {
	a := Attachment{Kind: KindAttachment, Name: "invoice.pdf", StoreRef: attTenant + "/journal-batch/2026/invoice.pdf", ContentType: "application/pdf", SizeBytes: 2048}
	a.Normalize()
	return a
}

func TestValidateAttachment_Table(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Attachment)
		field  string
	}{
		{"valid file with defaults", func(a *Attachment) {}, ""},
		{"dms store reference", func(a *Attachment) { a.StoreRef = "dms://archive/42" }, ""},
		{"dms store reference with a path", func(a *Attachment) { a.StoreRef = "dms://sharepoint-fin/sites/ap/doc-42.pdf" }, ""},
		{"https URL refused (SSRF)", func(a *Attachment) { a.StoreRef = "https://dms.example.com/doc/1" }, "storeRef"},
		{"metadata URL refused", func(a *Attachment) { a.StoreRef = "http://169.254.169.254/latest/meta-data" }, "storeRef"},
		{"file scheme refused", func(a *Attachment) { a.StoreRef = "file:///etc/passwd" }, "storeRef"},
		{"s3 URI refused", func(a *Attachment) { a.StoreRef = "s3://other-bucket/" + attTenant + "/x.pdf" }, "storeRef"},
		{"dms with user info refused", func(a *Attachment) { a.StoreRef = "dms://user@archive/42" }, "storeRef"},
		{"dms with port refused", func(a *Attachment) { a.StoreRef = "dms://archive:8080/42" }, "storeRef"},
		{"dms with query refused", func(a *Attachment) { a.StoreRef = "dms://archive/42?x=1" }, "storeRef"},
		{"dms traversal refused", func(a *Attachment) { a.StoreRef = "dms://archive/../42" }, "storeRef"},
		{"another tenant's key refused (IDOR)", func(a *Attachment) { a.StoreRef = "00000000-0000-4000-8000-000000000002/journal-batch/x.pdf" }, "storeRef"},
		{"key without a tenant prefix refused", func(a *Attachment) { a.StoreRef = "vouchers/2026/invoice.pdf" }, "storeRef"},
		{"bare tenant prefix refused", func(a *Attachment) { a.StoreRef = attTenant + "/" }, "storeRef"},
		{"leading slash refused", func(a *Attachment) { a.StoreRef = "/" + attTenant + "/x.pdf" }, "storeRef"},
		{"path traversal out of the tenant refused", func(a *Attachment) { a.StoreRef = attTenant + "/../other/x.pdf" }, "storeRef"},
		{"empty segment refused", func(a *Attachment) { a.StoreRef = attTenant + "//x.pdf" }, "storeRef"},
		{"encoded traversal refused", func(a *Attachment) { a.StoreRef = attTenant + "/%2e%2e/x.pdf" }, "storeRef"},
		{"over 1024 characters refused", func(a *Attachment) { a.StoreRef = attTenant + "/" + strings.Repeat("a/", 600) + "x" }, "storeRef"},
		{"no name", func(a *Attachment) { a.Name = "" }, "name"},
		{"path in name", func(a *Attachment) { a.Name = "../x.pdf" }, "name"},
		{"unknown document type", func(a *Attachment) { a.DocumentType = "selfie" }, "documentType"},
		{"unknown retention class", func(a *Attachment) { a.RetentionClass = "forever" }, "retentionClass"},
		{"content type not allowed", func(a *Attachment) { a.ContentType = "application/x-msdownload" }, "contentType"},
		{"empty file", func(a *Attachment) { a.SizeBytes = 0 }, "sizeBytes"},
		{"file over 25 MB", func(a *Attachment) { a.SizeBytes = MaxAttachmentBytes + 1 }, "sizeBytes"},
		{"exactly 25 MB", func(a *Attachment) { a.SizeBytes = MaxAttachmentBytes }, ""},
		{"file with note text", func(a *Attachment) { a.Note = "hi" }, "note"},
		{"unknown kind", func(a *Attachment) { a.Kind = "video" }, "kind"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := validFile()
			tc.mutate(&a)
			vs := ValidateAttachment(a, attTenant)
			if tc.field == "" {
				if len(vs) != 0 {
					t.Fatalf("want valid, got %+v", vs)
				}
				return
			}
			if len(vs) == 0 || vs[0].Field != tc.field || vs[0].Code != CodeAttachmentInvalid {
				t.Fatalf("want %s violation, got %+v", tc.field, vs)
			}
		})
	}
}

// A key is only valid for the tenant whose prefix it carries; no tenant means no key.
func TestValidStoreRef_TenantPrefix(t *testing.T) {
	key := attTenant + "/journal-batch/b1/invoice.pdf"
	if !ValidStoreRef(key, attTenant) || ValidStoreRef(key, "00000000-0000-4000-8000-000000000002") || ValidStoreRef(key, "") {
		t.Fatal("tenant prefix not enforced")
	}
}

func TestValidateAttachment_Notes(t *testing.T) {
	if vs := ValidateAttachment(Attachment{Kind: KindNote, Note: "Checked with the buyer"}, attTenant); len(vs) != 0 {
		t.Fatalf("valid note: %+v", vs)
	}
	if vs := ValidateAttachment(Attachment{Kind: KindNote}, attTenant); len(vs) != 1 {
		t.Fatalf("empty note: %+v", vs)
	}
	if vs := ValidateAttachment(Attachment{Kind: KindNote, Note: strings.Repeat("x", MaxNoteLength+1)}, attTenant); len(vs) != 1 {
		t.Fatalf("long note: %+v", vs)
	}
	if vs := ValidateAttachment(Attachment{Kind: KindNote, Note: "x", StoreRef: "k"}, attTenant); len(vs) != 1 || vs[0].Field != "kind" {
		t.Fatalf("note with file fields: %+v", vs)
	}
}

func TestNormalize_DefaultsAndRetention(t *testing.T) {
	a := validFile()
	if a.DocumentType != "other" || a.RetentionClass != "financial_record_10y" {
		t.Fatalf("defaults: %+v", a)
	}
	posted := time.Date(2026, 3, 31, 15, 0, 0, 0, time.UTC)
	for class, want := range map[string]string{"financial_record_10y": "2036-03-31", "financial_record_7y": "2033-03-31", "financial_record_6y": "2032-03-31", "working_paper_2y": "2028-03-31"} {
		if got := RetainedUntil(posted, class).Format(time.DateOnly); got != want {
			t.Errorf("%s: %s want %s", class, got, want)
		}
	}
	if !SameAttachment(a, validFile()) || SameAttachment(a, Attachment{Kind: KindNote}) {
		t.Fatal("SameAttachment")
	}
}
