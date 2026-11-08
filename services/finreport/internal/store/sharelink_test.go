package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

func shareSpec(lib, key string, days int) ShareSpec {
	return ShareSpec{LibraryID: lib, ExpiresInDays: days, Actor: "sharer", IdempotencyKey: key, RequestHash: "h-" + key}
}

// libraryIDs returns the csv and xlsx entry ids of the fixture's stored files.
func (f firingFixture) libraryIDs(t *testing.T) (csv, xlsx string) {
	t.Helper()
	page, err := f.st.ListLibrary(f.ctx, LibraryQuery{}, nil, 50)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("library %+v err %v", page.Items, err)
	}
	for _, e := range page.Items {
		if e.Format == "csv" {
			csv = e.ID
		} else {
			xlsx = e.ID
		}
	}
	return csv, xlsx
}

func TestShareLink_CreateReplayRedeemAndCount(t *testing.T) {
	f := firingSetup(t)
	f.stored(t, time.Now().UTC().AddDate(5, 0, 0))
	csv, _ := f.libraryIDs(t)

	l, err := f.st.CreateShareLink(f.ctx, shareSpec(csv, "k1", 14))
	if err != nil || l.Status != "active" || l.Revision != 1 || l.Token == "" || l.CreatedBy != "sharer" || l.LibraryID != csv || l.LegalEntityID != f.entity || l.Replayed {
		t.Fatalf("link %+v err %v", l, err)
	}
	if d := time.Until(l.ExpiresAt); d < 13*24*time.Hour || d > 14*24*time.Hour+time.Minute {
		t.Fatalf("expires in %s, want 14 days", d)
	}
	// Only the hash of the secret is stored.
	var hash string
	if err := f.admin.QueryRow(f.ctx, `select token_hash from report_share_link where id=$1::uuid`, l.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 || strings.Contains(l.Token, hash) {
		t.Fatalf("stored hash %q", hash)
	}

	// A retry returns the stored link without the secret; another request under the key is refused.
	again, err := f.st.CreateShareLink(f.ctx, shareSpec(csv, "k1", 14))
	if err != nil || again.ID != l.ID || !again.Replayed || again.Token != "" {
		t.Fatalf("replay %+v err %v", again, err)
	}
	other := shareSpec(csv, "k1", 14)
	other.RequestHash = "different"
	_, err = f.st.CreateShareLink(f.ctx, other)
	wantProblem(t, err, 422, CodeIdempotencyReuse)
	for _, days := range []int{0, 91, -1} {
		_, err = f.st.CreateShareLink(f.ctx, shareSpec(csv, "kd", days))
		wantProblem(t, err, 422, CodeShareDays)
	}
	_, err = f.st.CreateShareLink(f.ctx, shareSpec(ids.New(), "kn", 14))
	wantProblem(t, err, 404, CodeLibraryNotFound)

	// The holder of the token downloads the exact file, with no principal of their own.
	sf, err := f.st.RedeemShareLink(context.Background(), l.Token)
	if err != nil || string(sf.Content) != "aaaa" || sf.Entry.ID != csv || sf.Entry.SHA256 == "" || sf.Entry.Format != "csv" || sf.LinkID != l.ID {
		t.Fatalf("redeem %+v err %v", sf, err)
	}
	if _, err := f.st.RedeemShareLink(context.Background(), l.Token); err != nil {
		t.Fatal(err)
	}
	links, err := f.st.ListShareLinks(f.ctx, csv)
	if err != nil || len(links) != 1 || links[0].DownloadCount != 2 || links[0].LastDownloadedAt == nil || links[0].Token != "" {
		t.Fatalf("list %+v err %v", links, err)
	}
	if other, err := f.st.ListShareLinks(f.ctx, mustOtherID(t, f)); err != nil || len(other) != 0 {
		t.Fatalf("the other file has no links: %+v %v", other, err)
	}

	// Wrong secrets, other shapes and another tenant's token open nothing.
	tenantPart := l.Token[:strings.Index(l.Token, ".")]
	for name, bad := range map[string]string{
		"garbage":       "nope",
		"wrong secret":  tenantPart + "." + strings.Repeat("A", 43),
		"other tenant":  strings.ReplaceAll(testkit.TenantID(2), "-", "") + l.Token[strings.Index(l.Token, "."):],
		"empty":         "",
		"tenant only":   tenantPart,
		"secret padded": l.Token + "=",
	} {
		_, err := f.st.RedeemShareLink(context.Background(), bad)
		if s, c := problemCodeOf(err); s != 404 || c != CodeShareNotFound {
			t.Errorf("%s: err %v, want 404 %s", name, err, CodeShareNotFound)
		}
	}

	types := outboxTypes(t, f.ctx, f.admin)
	if countType(types, events.ShareLinkCreated) != 1 {
		t.Fatalf("outbox %v: want one sharelink.created", types)
	}
	var le, secretLeak string
	if err := f.admin.QueryRow(f.ctx, `select coalesce(payload->>'legalentityid',''), payload::text from outbox where type=$1`, events.ShareLinkCreated).Scan(&le, &secretLeak); err != nil || le != f.entity {
		t.Fatalf("legalentityid %q err %v", le, err)
	}
	if strings.Contains(secretLeak, l.Token[strings.Index(l.Token, ".")+1:]) {
		t.Fatal("the secret is in the event")
	}
	var audits int
	if err := f.admin.QueryRow(f.ctx, `select count(*) from audit_trail_entry where kind='share_link_created' and object_ref=$1`, "share_link:"+l.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audit entries %d err %v", audits, err)
	}
}

func mustOtherID(t *testing.T, f firingFixture) string {
	t.Helper()
	csv, xlsx := f.libraryIDs(t)
	_ = csv
	return xlsx
}

func TestShareLink_ExpiredAndRevokedLinksAnswerGone(t *testing.T) {
	f := firingSetup(t)
	f.stored(t, time.Now().UTC().AddDate(5, 0, 0))
	csv, _ := f.libraryIDs(t)

	expiring, err := f.st.CreateShareLink(f.ctx, shareSpec(csv, "k1", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `update report_share_link set created_at = now() - interval '2 days', expires_at = now() - interval '1 day' where id=$1::uuid`, expiring.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.st.RedeemShareLink(context.Background(), expiring.Token)
	wantProblem(t, err, 410, CodeShareGone)
	if links, _ := f.st.ListShareLinks(f.ctx, csv); len(links) != 1 || links[0].Status != "expired" || links[0].DownloadCount != 0 {
		t.Fatalf("expired link %+v", links)
	}

	l, err := f.st.CreateShareLink(f.ctx, shareSpec(csv, "k2", 7))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RedeemShareLink(context.Background(), l.Token); err != nil {
		t.Fatal(err)
	}
	// If-Match is required and checked.
	_, err = f.st.RevokeShareLink(f.ctx, l.ID, nil, "revoker")
	wantProblem(t, err, 428, "precondition_required")
	stale := 7
	_, err = f.st.RevokeShareLink(f.ctx, l.ID, &stale, "revoker")
	wantProblem(t, err, 412, CodeRevisionConflict)
	_, err = f.st.RevokeShareLink(f.ctx, ids.New(), &l.Revision, "revoker")
	wantProblem(t, err, 404, CodeShareNotFound)
	rev, err := f.st.RevokeShareLink(f.ctx, l.ID, &l.Revision, "revoker")
	if err != nil || rev.Status != "revoked" || rev.Revision != 2 || rev.RevokedBy != "revoker" || rev.RevokedAt == nil || rev.DownloadCount != 1 {
		t.Fatalf("revoked %+v err %v", rev, err)
	}
	_, err = f.st.RedeemShareLink(context.Background(), l.Token)
	wantProblem(t, err, 410, CodeShareGone)
	_, err = f.st.RevokeShareLink(f.ctx, l.ID, &rev.Revision, "revoker")
	wantProblem(t, err, 409, CodeShareRevoked)

	types := outboxTypes(t, f.ctx, f.admin)
	if countType(types, events.ShareLinkRevoked) != 1 || countType(types, events.ShareLinkCreated) != 2 {
		t.Fatalf("outbox %v", types)
	}
	var audits int
	if err := f.admin.QueryRow(f.ctx, `select count(*) from audit_trail_entry where kind='share_link_revoked'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("revoke audit entries %d err %v", audits, err)
	}
}

func TestShareLink_NeverOutlivesTheFile(t *testing.T) {
	f := firingSetup(t)
	soon := time.Now().UTC().Add(48 * time.Hour)
	f.stored(t, soon) // csv retained for two more days, xlsx a year longer
	csv, xlsx := f.libraryIDs(t)

	short, err := f.st.CreateShareLink(f.ctx, shareSpec(csv, "k1", 30))
	if err != nil || short.ExpiresAt.After(soon.Add(2*time.Second)) {
		t.Fatalf("link %+v err %v: want expiry capped at the retention date %s", short, err, soon)
	}
	long, err := f.st.CreateShareLink(f.ctx, shareSpec(xlsx, "k2", 30))
	if err != nil || time.Until(long.ExpiresAt) < 29*24*time.Hour {
		t.Fatalf("link %+v err %v", long, err)
	}

	// A file past its retention date cannot be shared.
	if _, err := f.admin.Exec(f.ctx, `update report_library_entry set retain_until = now() - interval '1 hour' where id=$1::uuid`, csv); err != nil {
		t.Fatal(err)
	}
	_, err = f.st.CreateShareLink(f.ctx, shareSpec(csv, "k3", 5))
	wantProblem(t, err, 409, CodeShareRetention)

	// When the sweep deletes the file its links go with it.
	if passes, err := f.st.SweepLibrary(f.ctx, time.Now()); err != nil || len(passes) != 1 || passes[0].DeletedCount != 1 {
		t.Fatalf("sweep %+v err %v", passes, err)
	}
	_, err = f.st.RedeemShareLink(context.Background(), short.Token)
	wantProblem(t, err, 404, CodeShareNotFound)
	if _, err := f.st.RedeemShareLink(context.Background(), long.Token); err != nil {
		t.Fatalf("the other file's link still works: %v", err)
	}
}

func TestShareLink_LegalEntityScopeHidesLinks(t *testing.T) {
	f := firingSetup(t)
	f.stored(t, time.Now().UTC().AddDate(5, 0, 0))
	csv, _ := f.libraryIDs(t)
	l, err := f.st.CreateShareLink(f.ctx, shareSpec(csv, "k1", 7))
	if err != nil {
		t.Fatal(err)
	}
	other := scopedCtx(f.tenantID(), ids.New())
	_, err = f.st.ListShareLinks(other, csv)
	wantProblem(t, err, 404, CodeLibraryNotFound)
	_, err = f.st.CreateShareLink(other, shareSpec(csv, "k9", 7))
	wantProblem(t, err, 404, CodeLibraryNotFound)
	_, err = f.st.RevokeShareLink(other, l.ID, &l.Revision, "x")
	wantProblem(t, err, 404, CodeShareNotFound)
}

// A revocation that commits while a download is in flight wins: the download
// waits for the link row and then finds it revoked, serves nothing and counts
// nothing.
func TestShareLink_ARevocationCommittedFirstStopsAConcurrentDownload(t *testing.T) {
	f := firingSetup(t)
	f.stored(t, time.Now().UTC().AddDate(5, 0, 0))
	csv, _ := f.libraryIDs(t)
	l, err := f.st.CreateShareLink(f.ctx, shareSpec(csv, "k1", 7))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(f.ctx, `update report_share_link set revoked_at=now(), revoked_by='racer', revision=revision+1 where id=$1::uuid`, l.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.st.RedeemShareLink(context.Background(), l.Token)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the download did not wait for the revocation in flight: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, <-done, 410, CodeShareGone)
	var count int
	if err := f.admin.QueryRow(f.ctx, `select download_count from report_share_link where id=$1::uuid`, l.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("download count %d err %v, want 0", count, err)
	}
}
