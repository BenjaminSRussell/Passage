package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// ReauthHeader carries the step-up assertion of a signing (21 CFR 11.200), the
// same header engineering's document signing reads.
const ReauthHeader = "X-Passage-Reauth-Assertion"

const (
	maxPrintedName = 200
	maxMethod      = 64
)

// Reauthenticator is the step-up seam (11.200): it proves the actor just
// re-authenticated, with a proof bound to the record and the hash of what is
// signed. It returns the method it verified; that, never the client's claim,
// is what the signature records. A proof that does not hold is a
// *domain.Error with PMF-0010, which counts towards the BR-24.14 lockout; any
// other error (the verifier is down) does not.
type Reauthenticator interface {
	Verify(ctx context.Context, actor string, auth domain.Reauth) (method string, err error)
}

// FailClosedReauth is bound when no step-up verifier is configured: signing
// answers 503, so no batch record is signed without a re-authentication.
type FailClosedReauth struct{}

func (FailClosedReauth) Verify(context.Context, string, domain.Reauth) (string, error) {
	return "", httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeSignatureUnavailable,
		"signing is unavailable: no re-authentication service is configured")
}

// DevReauth verifies nothing; it is bound only when PASSAGE_ENV=dev, and every
// signature it admits records domain.AuthDevUnverified, so a sealed record
// signed with it says signaturesVerified false.
type DevReauth struct{}

func (DevReauth) Verify(_ context.Context, actor string, auth domain.Reauth) (string, error) {
	if actor == "" || auth.UserID != actor {
		return "", domain.CheckSignerSubject(actor, auth.UserID)
	}
	return domain.AuthDevUnverified, nil
}

// signRequest is the part of a signing command the signer types.
type signRequest struct {
	PrintedName string `json:"printedName"`
	Method      string `json:"method"`
}

func (s *signRequest) check(fields map[string]string) {
	s.PrintedName = strings.TrimSpace(s.PrintedName)
	if n := utf8.RuneCountInString(s.PrintedName); n < 1 || n > maxPrintedName {
		fields["printedName"] = fmt.Sprintf("the signer's printed name, 1 to %d characters", maxPrintedName)
	}
	if len(s.Method) > maxMethod {
		fields["method"] = fmt.Sprintf("at most %d characters", maxMethod)
	}
}

// reauthenticate runs the step-up for p over hash, with the BR-24.14 lockout:
// a locked signer is refused before the verifier is asked (423 PMF-0011), a
// rejected proof is counted (the third in a row locks signing for 15 minutes)
// and answers 403 PMF-0010, and a success clears the count.
func (a *API) reauthenticate(ctx context.Context, p tenant.Principal, recordID string, hash []byte, req signRequest, assertion string) (store.Signing, error) {
	if p.UserID == "" {
		return store.Signing{}, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "signing a batch record needs the acting user")
	}
	now := a.now()
	l, err := a.store.SignerLockout(ctx, p.UserID)
	if err != nil {
		return store.Signing{}, err
	}
	if err := l.CheckAttempt(now); err != nil {
		return store.Signing{}, err
	}
	method, err := a.reauth.Verify(ctx, p.UserID, domain.Reauth{UserID: p.UserID, Assertion: assertion, Method: req.Method, RecordID: recordID, RecordHash: hash})
	var de *domain.Error
	if err == nil && method == "" {
		err = domain.CheckSignerSubject("", p.UserID)
	}
	if errors.As(err, &de) && de.Code == domain.CodeSignatureRejected {
		after, lerr := a.store.RecordReauthResult(ctx, p.UserID, true, now)
		if lerr != nil {
			return store.Signing{}, lerr
		}
		if locked := after.CheckAttempt(now); locked != nil {
			return store.Signing{}, locked
		}
		return store.Signing{}, err
	}
	if err != nil {
		return store.Signing{}, err
	}
	if l.Failures > 0 {
		if _, err := a.store.RecordReauthResult(ctx, p.UserID, false, now); err != nil {
			return store.Signing{}, err
		}
	}
	return store.Signing{PrintedName: req.PrintedName, Method: method, RecordHash: hash}, nil
}
