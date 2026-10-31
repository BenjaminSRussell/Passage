package domain_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

var signer = domain.HMACSigner{Key: []byte("test-key")}

func rec(key string, body string) domain.PackageRecord {
	return domain.PackageRecord{Key: key, Body: json.RawMessage(body)}
}

func sealed(t interface {
	Helper()
	Fatal(...any)
}, files []domain.PackageFile, deps map[string][]string) (domain.Package, []byte) {
	t.Helper()
	p := domain.Package{Version: "1.0.0", SchemaVersion: 1, Files: files}
	for _, f := range files {
		p.Manifest.Files = append(p.Manifest.Files, domain.ManifestFile{Name: f.Name, DependsOn: deps[f.Name]})
	}
	p, err := domain.Seal(p)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := domain.Sign(p, signer)
	if err != nil {
		t.Fatal(err)
	}
	return p, sig
}

func smallPackage(t *testing.T) (domain.Package, []byte) {
	return sealed(t, []domain.PackageFile{
		{Name: "uom.json", Entity: "uom", Records: []domain.PackageRecord{rec("EA", `{"name":"Each","scale":0}`)}},
		{Name: "item.json", Entity: "item", Records: []domain.PackageRecord{rec("I1", `{"name":"Bolt","uom":"EA"}`)}},
	}, map[string][]string{"item.json": {"uom.json"}})
}

func code(err error) string {
	if e, ok := err.(*domain.Error); ok {
		return e.Code
	}
	return fmt.Sprint(err)
}

func TestVerify_SignedPackageAndTampering(t *testing.T) {
	p, sig := smallPackage(t)
	if err := domain.Verify(p, sig, signer); err != nil {
		t.Fatalf("signed package must verify: %v", err)
	}
	tamper := map[string]func(domain.Package, []byte) (domain.Package, []byte){
		"file body": func(p domain.Package, s []byte) (domain.Package, []byte) {
			p.Files = append([]domain.PackageFile(nil), p.Files...)
			p.Files[0].Records = []domain.PackageRecord{rec("EA", `{"name":"Eacn","scale":0}`)}
			return p, s
		},
		"manifest": func(p domain.Package, s []byte) (domain.Package, []byte) {
			p.Manifest.Files = append([]domain.ManifestFile(nil), p.Manifest.Files...)
			p.Manifest.Files[0].SHA256 = strings.Repeat("0", 64)
			return p, s
		},
		"signature": func(p domain.Package, s []byte) (domain.Package, []byte) {
			s = append([]byte(nil), s...)
			s[0] ^= 1
			return p, s
		},
		"schema relabel": func(p domain.Package, s []byte) (domain.Package, []byte) { p.SchemaVersion = 0; return p, s },
	}
	for name, f := range tamper {
		tp, ts := f(p, sig)
		if err := domain.Verify(tp, ts, signer); code(err) != domain.CodePkgSignature {
			t.Errorf("%s: want %s, got %v", name, domain.CodePkgSignature, err)
		}
	}
	if err := domain.Verify(p, sig, domain.HMACSigner{Key: []byte("other")}); code(err) != domain.CodePkgSignature {
		t.Errorf("wrong key must fail, got %v", err)
	}
}

func TestCheckSchema(t *testing.T) {
	p := domain.Package{SchemaVersion: 3}
	if code(domain.CheckSchema(p, 2)) != domain.CodePkgSchemaNewer {
		t.Fatal("newer schema must be refused")
	}
	if err := domain.CheckSchema(domain.Package{SchemaVersion: 2}, 2); err != nil {
		t.Fatal(err)
	}
	if code(domain.CheckSchema(domain.Package{}, 2)) != domain.CodePkgInvalid {
		t.Fatal("schema 0 must be invalid")
	}
}

func TestVerify_InvalidManifests(t *testing.T) {
	file := func(n string) domain.PackageFile {
		return domain.PackageFile{Name: n, Entity: n, Records: []domain.PackageRecord{rec("k", `{"a":1}`)}}
	}
	cases := map[string]struct {
		files []domain.PackageFile
		deps  map[string][]string
	}{
		"cycle":      {[]domain.PackageFile{file("a"), file("b")}, map[string][]string{"a": {"b"}, "b": {"a"}}},
		"self":       {[]domain.PackageFile{file("a")}, map[string][]string{"a": {"a"}}},
		"unknown":    {[]domain.PackageFile{file("a")}, map[string][]string{"a": {"ghost"}}},
		"dup record": {[]domain.PackageFile{{Name: "a", Entity: "a", Records: []domain.PackageRecord{rec("k", `{}`), rec("k", `{}`)}}}, nil},
	}
	for name, c := range cases {
		p := domain.Package{Version: "1", SchemaVersion: 1, Files: c.files}
		for _, f := range c.files {
			p.Manifest.Files = append(p.Manifest.Files, domain.ManifestFile{Name: f.Name, DependsOn: c.deps[f.Name]})
		}
		p, err := domain.Seal(p)
		if err != nil {
			t.Fatal(err)
		}
		sig, _ := domain.Sign(p, signer)
		if err := domain.Verify(p, sig, signer); code(err) != domain.CodePkgInvalid {
			t.Errorf("%s: want %s, got %v", name, domain.CodePkgInvalid, err)
		}
	}
	p, sig := smallPackage(t)
	p.Files = p.Files[:1]
	if err := domain.Verify(p, sig, signer); code(err) != domain.CodePkgInvalid {
		t.Errorf("manifest file without a package file must be invalid, got %v", err)
	}
}

func TestVerify_ErrorsNeverIncludeRecordBodies(t *testing.T) {
	p, sig := smallPackage(t)
	p.Files = append([]domain.PackageFile(nil), p.Files...)
	p.Files[1].Records = []domain.PackageRecord{rec("I1", `{"name":"SECRET-FORMULA","uom":"EA"}`)}
	err := domain.Verify(p, sig, signer)
	if err == nil || strings.Contains(err.Error(), "SECRET-FORMULA") {
		t.Fatalf("error must exist and not leak the body: %v", err)
	}
	bad := p
	bad.Files[0].Records = []domain.PackageRecord{rec("EA", `["SECRET-FORMULA"`)}
	if _, err := domain.Seal(bad); err == nil || strings.Contains(err.Error(), "SECRET-FORMULA") {
		t.Fatalf("seal error must not leak the body: %v", err)
	}
}

func bigPackage(t *testing.T) (domain.Package, []byte) {
	var recs []domain.PackageRecord
	for i := 0; i < 240; i++ {
		recs = append(recs, rec(fmt.Sprintf("K%03d", i), fmt.Sprintf(`{"name":"n%d","qty":%d.50}`, i, i)))
	}
	return sealed(t, []domain.PackageFile{{Name: "setup.json", Entity: "setup", Records: recs}}, nil)
}

func TestApply_DiffCountsIdempotenceAndNoDeletes(t *testing.T) {
	p, sig := bigPackage(t)
	target := domain.NewMemPackageTarget()
	target.Data["setup"] = map[string]map[string]any{}
	for i := 0; i < 40; i++ { // identical (target holds the number as a float and without the trailing zero)
		target.Data["setup"][fmt.Sprintf("K%03d", i)] = map[string]any{"name": fmt.Sprintf("n%d", i), "qty": float64(i) + 0.5}
	}
	for i := 40; i < 50; i++ { // differing
		target.Data["setup"][fmt.Sprintf("K%03d", i)] = map[string]any{"name": "old", "qty": 1}
	}
	target.Data["setup"]["ONLY-IN-TARGET"] = map[string]any{"name": "keep"}
	ctx := context.Background()

	dry, err := domain.Apply(ctx, p, sig, signer, target, domain.ApplyOptions{DryRun: true})
	if err != nil || dry.Create != 190 || dry.Update != 10 || dry.Unchanged != 40 {
		t.Fatalf("dry run = %+v, %v", dry, err)
	}
	if len(target.Upserts) != 0 || len(target.Data["setup"]) != 51 {
		t.Fatal("dry run must write nothing")
	}
	diff, err := domain.Diff(ctx, p, sig, signer, target, 1)
	if err != nil || !reflect.DeepEqual(diff, dry) {
		t.Fatalf("diff must equal the dry run: %+v vs %+v", diff, dry)
	}
	got, err := domain.Apply(ctx, p, sig, signer, target, domain.ApplyOptions{})
	if err != nil || !reflect.DeepEqual(got, dry) {
		t.Fatalf("apply = %+v, %v", got, err)
	}
	if _, ok := target.Data["setup"]["ONLY-IN-TARGET"]; !ok || len(target.Data["setup"]) != 241 {
		t.Fatal("records only in the target must stay")
	}
	again, err := domain.Apply(ctx, p, sig, signer, target, domain.ApplyOptions{})
	if err != nil || again.Changes() != 0 || again.Unchanged != 240 {
		t.Fatalf("second apply = %+v, %v", again, err)
	}
	if len(target.Upserts) != 1 {
		t.Fatalf("second apply must not write, upserts = %v", target.Upserts)
	}
}

func TestApply_WritesInDependencyOrderAndRefusesBeforeWriting(t *testing.T) {
	// manifest lists the dependent first; apply must still write uom before item.
	p := domain.Package{Version: "1", SchemaVersion: 1, Files: []domain.PackageFile{
		{Name: "item.json", Entity: "item", Records: []domain.PackageRecord{rec("I1", `{"uom":"EA"}`)}},
		{Name: "uom.json", Entity: "uom", Records: []domain.PackageRecord{rec("EA", `{"name":"Each"}`)}},
	}, Manifest: domain.Manifest{Files: []domain.ManifestFile{{Name: "item.json", DependsOn: []string{"uom.json"}}, {Name: "uom.json"}}}}
	p, _ = domain.Seal(p)
	sig, _ := domain.Sign(p, signer)
	target := domain.NewMemPackageTarget()
	if _, err := domain.Apply(context.Background(), p, sig, signer, target, domain.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(target.Upserts, []string{"uom", "item"}) {
		t.Fatalf("write order = %v", target.Upserts)
	}
	tampered := p
	tampered.Version = "2"
	other := domain.NewMemPackageTarget()
	if _, err := domain.Apply(context.Background(), tampered, sig, signer, other, domain.ApplyOptions{}); code(err) != domain.CodePkgSignature || len(other.Upserts) != 0 {
		t.Fatalf("tampered package must write nothing, got %v %v", err, other.Upserts)
	}
	newer := p
	newer.SchemaVersion = 9
	newerSig, _ := domain.Sign(newer, signer)
	if _, err := domain.Apply(context.Background(), newer, newerSig, signer, other, domain.ApplyOptions{}); code(err) != domain.CodePkgSchemaNewer || len(other.Upserts) != 0 {
		t.Fatalf("newer schema must write nothing, got %v", err)
	}
}

func TestPackage_CanonicalHashIgnoresKeyOrderAndWhitespace(t *testing.T) {
	a, _ := sealed(t, []domain.PackageFile{{Name: "f", Entity: "e", Records: []domain.PackageRecord{rec("k", `{"a":1,"b":[1.0,2]}`)}}}, nil)
	b, _ := sealed(t, []domain.PackageFile{{Name: "f", Entity: "e", Records: []domain.PackageRecord{rec("k", ` { "b" : [1,2], "a": 1.0 } `)}}}, nil)
	if a.Manifest.Files[0].SHA256 != b.Manifest.Files[0].SHA256 {
		t.Fatal("canonical hash must not depend on formatting")
	}
}

func TestPackage_SignVerifyRoundTripProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "files")
		var files []domain.PackageFile
		for i := 0; i < n; i++ {
			var recs []domain.PackageRecord
			for j := 0; j < rapid.IntRange(0, 4).Draw(t, "records"); j++ {
				body, _ := json.Marshal(map[string]string{"v": rapid.String().Draw(t, "v")})
				recs = append(recs, rec(fmt.Sprintf("k%d", j), string(body)))
			}
			files = append(files, domain.PackageFile{Name: fmt.Sprintf("f%d", i), Entity: fmt.Sprintf("e%d", i), Records: recs})
		}
		p, sig := sealed(t, files, nil)
		if err := domain.Verify(p, sig, signer); err != nil {
			t.Fatal(err)
		}
		target := domain.NewMemPackageTarget()
		if _, err := domain.Apply(context.Background(), p, sig, signer, target, domain.ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
		again, err := domain.Apply(context.Background(), p, sig, signer, target, domain.ApplyOptions{})
		if err != nil || again.Changes() != 0 {
			t.Fatalf("apply must be idempotent: %+v %v", again, err)
		}
	})
}
