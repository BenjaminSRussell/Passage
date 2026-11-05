package delivery

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

func TestBackoffDoublesFromAMinuteToAnHour(t *testing.T) {
	want := []time.Duration{time.Minute, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for n, w := range want {
		if got := Backoff(n); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, w)
		}
	}
}

func TestMDNTargetAllowedOnlyOnThePartnersAS2Host(t *testing.T) {
	partner := "https://as2.distributor.example:4443/as2/inbound"
	for target, want := range map[string]bool{
		"https://as2.distributor.example:4443/mdn":            true,
		"https://AS2.Distributor.example:4443/other/path?x=1": true,
		"https://as2.distributor.example/mdn":                 false, // other port
		"http://as2.distributor.example:4443/mdn":             false, // other scheme
		"https://evil.example/mdn":                            false,
		"https://user:pw@as2.distributor.example:4443/mdn":    false,
		"https://as2.distributor.example:4443.evil.example/":  false,
		"mailto:mdn@as2.distributor.example":                  false,
		"":                                                    false,
		"::not a url":                                         false,
	} {
		if got := MDNTargetAllowed(target, partner); got != want {
			t.Errorf("%q: %v, want %v", target, got, want)
		}
	}
	if MDNTargetAllowed("https://as2.distributor.example:4443/mdn", "") {
		t.Error("allowed without a partner URL")
	}
}

func TestInboxFileSkipsPartialsDirectoriesAndOurFolders(t *testing.T) {
	cfg := domain.SFTPSettings{ArchiveDir: "processed", FailedDir: "failed"}
	for name, want := range map[string]bool{
		"po.x12": true, "po.x12.part": false, "processed": false, "failed": false, "sub/": false, ".hidden": false, "": false, "a/b": false,
	} {
		if got := inboxFile(name, cfg); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}
