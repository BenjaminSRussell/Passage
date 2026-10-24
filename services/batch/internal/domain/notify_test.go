package domain

import (
	"testing"
	"time"
)

func TestValidatePlaceholders(t *testing.T) {
	allowed := map[string]bool{"record.number": true, "link": true}
	if err := ValidatePlaceholders("Approve {{record.number}}", "Open {{link}}", allowed); err != nil {
		t.Fatal(err)
	}
	err := ValidatePlaceholders("Hi", "Missing {{record.nonexistent}}", allowed)
	if err == nil || err.(*Error).Code != CodeUnknownPlaceholder {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderFailClosed(t *testing.T) {
	out, err := Render("PO {{record.number}} {{link}}", map[string]string{"record.number": "PR-1", "link": "https://x"})
	if err != nil || out != "PO PR-1 https://x" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := Render("{{missing}}", map[string]string{}); err == nil || err.(*Error).Code != CodeRenderFailed {
		t.Fatalf("err = %v", err)
	}
}

func TestNextAttempt(t *testing.T) {
	now := time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)
	at, more := NextAttempt(1, now)
	if !more || !at.Equal(now.Add(time.Minute)) {
		t.Fatalf("%v %v", at, more)
	}
	if _, more := NextAttempt(5, now); more {
		t.Fatal("fifth attempt is the last")
	}
	if _, more := NextAttempt(6, now); more {
		t.Fatal("no sixth retry")
	}
}

func TestHardBounce(t *testing.T) {
	n, bouncing := HardBounce(2)
	if n != 3 || !bouncing {
		t.Fatalf("%d %v", n, bouncing)
	}
	if _, bouncing := HardBounce(1); bouncing {
		t.Fatal("two bounces are not enough")
	}
}

func TestDigestAndEscalationEmail(t *testing.T) {
	pref := Preference{Channels: []string{ChannelInApp, ChannelEmail}, Digest: DigestDaily}
	if ShouldEmail(pref, "workflow_assigned") {
		t.Fatal("digest suppresses per-item email")
	}
	if !ShouldEmail(pref, "workflow_escalated") {
		t.Fatal("escalations are immediate")
	}
	if !DigestEligible(true, false) || DigestEligible(true, true) {
		t.Fatal("digest keeps unread only")
	}
}

func TestAllowlisted(t *testing.T) {
	if !Allowlisted("a@passage.dev", []string{"passage.dev"}) {
		t.Fatal("expected allow")
	}
	if Allowlisted("a@example.com", []string{"passage.dev"}) {
		t.Fatal("expected deny")
	}
}

func TestShippedTemplates(t *testing.T) {
	if len(ShippedTemplates()) != 7 {
		t.Fatalf("%v", ShippedTemplates())
	}
}
