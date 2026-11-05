package api

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/certtrust"
	"github.com/BenjaminSRussell/passage/services/edi/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/secrets"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// Transport error codes.
const (
	CodeNotResendable        = "EDI-0015"
	CodeTransportUnavailable = "EDI-0016"
)

type as2InboundView struct {
	EndpointID               string    `json:"endpointId"`
	URL                      string    `json:"url,omitempty"`
	MDNURL                   string    `json:"mdnUrl,omitempty"`
	PartnerCertificateSHA256 string    `json:"partnerCertificateSha256"`
	PartnerCertificateExpiry time.Time `json:"partnerCertificateNotAfter"`
	// PartnerCertificateTrust is "pinned" without trust roots, otherwise
	// "trusted", or "untrusted" or "revocation-unknown" with the reason in
	// PartnerCertificateTrustError.
	PartnerCertificateTrust      string `json:"partnerCertificateTrust"`
	PartnerCertificateTrustError string `json:"partnerCertificateTrustError,omitempty"`
}

type transportView struct {
	PartnerID     string                  `json:"partnerId"`
	LegalEntityID string                  `json:"legalEntityId"`
	Kind          string                  `json:"kind"`
	AS2           *domain.AS2Settings     `json:"as2,omitempty"`
	SFTP          *domain.SFTPSettings    `json:"sftp,omitempty"`
	AS2Inbound    *as2InboundView         `json:"as2Inbound,omitempty"`
	Version       int                     `json:"version"`
	UpdatedAt     time.Time               `json:"updatedAt"`
	Secrets       map[string]secretStatus `json:"secrets,omitempty"`
}

// secretStatus says whether a referenced secret is present and usable,
// without anything of its content.
type secretStatus struct {
	Ref    string `json:"ref"`
	Status string `json:"status"`
}

func (a *API) transportView(t store.TransportRecord) transportView {
	v := transportView{PartnerID: t.PartnerID, LegalEntityID: t.LegalEntityID, Kind: t.Config.Kind, AS2: t.Config.AS2, SFTP: t.Config.SFTP,
		Version: t.Version, UpdatedAt: t.UpdatedAt}
	if t.Config.AS2 != nil {
		in := &as2InboundView{EndpointID: t.EndpointID}
		if a.AS2PublicURL != "" {
			in.URL = a.AS2PublicURL + "/as2/v1/" + t.EndpointID
			in.MDNURL = delivery.AS2MDNURL(a.AS2PublicURL, t.EndpointID)
		}
		if cert, intermediates, msg := domain.ParsePartnerChain(t.Config.AS2.PartnerCertificate, a.now()); msg == "" {
			in.PartnerCertificateSHA256 = domain.CertificateFingerprint(cert)
			in.PartnerCertificateExpiry = cert.NotAfter
			in.PartnerCertificateTrust = "pinned"
			if a.AS2Trust != nil {
				switch err := a.AS2Trust.Verify(cert, intermediates, a.now()); {
				case err == nil:
					in.PartnerCertificateTrust = "trusted"
				case errors.Is(err, certtrust.ErrUntrusted):
					in.PartnerCertificateTrust, in.PartnerCertificateTrustError = "untrusted", err.Error()
				default:
					in.PartnerCertificateTrust, in.PartnerCertificateTrustError = "revocation-unknown", err.Error()
				}
			}
		}
		v.AS2Inbound = in
	}
	if a.Secrets != nil {
		v.Secrets = a.checkSecrets(t.Config)
	}
	return v
}

// checkSecrets resolves the references of a transport and reports, per JSON
// field, whether each is present and of the right kind.
func (a *API) checkSecrets(cfg domain.PartnerTransport) map[string]secretStatus {
	out := map[string]secretStatus{}
	status := func(err error) string {
		switch {
		case err == nil:
			return "ok"
		case errors.Is(err, secrets.ErrNotFound):
			return "missing"
		default:
			return "malformed"
		}
	}
	if cfg.AS2 != nil {
		_, _, err := secrets.AS2Identity(a.Secrets, cfg.AS2.SigningKeyRef)
		out["as2.signingKeyRef"] = secretStatus{Ref: cfg.AS2.SigningKeyRef, Status: status(err)}
	}
	if cfg.SFTP != nil {
		var err error
		if cfg.SFTP.AuthMethod == "password" {
			_, err = secrets.Password(a.Secrets, cfg.SFTP.CredentialRef)
		} else {
			_, err = secrets.SSHSigner(a.Secrets, cfg.SFTP.CredentialRef)
		}
		out["sftp.credentialRef"] = secretStatus{Ref: cfg.SFTP.CredentialRef, Status: status(err)}
	}
	return out
}

// putTransport selects the partner's transport and its settings. The
// transport is bound to the legal entity of the partner's trade profile, so
// the caller must be scoped to that entity, as for the profile. Secrets are
// referenced by name only; a reference that does not resolve to a usable
// secret in EDI_SECRETS_DIR is refused.
func (a *API) putTransport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var cfg domain.PartnerTransport
	if err := httpx.Decode(r, &cfg); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cfg = cfg.WithDefaults()
	rules := domain.TransportRules{Now: a.now(), AllowHTTP: a.AllowInsecureAS2, AsyncMDN: a.AS2PublicURL != ""}
	if a.AS2Trust != nil {
		// Fail closed: a certificate that does not chain to the trust roots,
		// is revoked, or whose revocation status is unknown is not saved.
		rules.TrustCertificate = func(leaf *x509.Certificate, intermediates []*x509.Certificate) string {
			if err := a.AS2Trust.Verify(leaf, intermediates, a.now()); err != nil {
				return err.Error()
			}
			return ""
		}
	}
	fields := cfg.Validate(rules)
	if len(fields) == 0 && cfg.Kind != domain.TransportAPI {
		if a.Secrets == nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeTransportUnavailable, "no secret directory is configured (EDI_SECRETS_DIR); AS2 and SFTP cannot be used"))
			return
		}
		for field, st := range a.checkSecrets(cfg) {
			if st.Status != "ok" {
				fields[field] = fmt.Sprintf("secret %q is %s", st.Ref, st.Status)
			}
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	rec, err := a.store.PutTransport(r.Context(), id, cfg)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	if errors.Is(err, store.ErrNoTradeProfile) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile in your legal entities; PUT /partners/{id}/trade-profile first"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, rec.Version))
	httpx.JSON(w, http.StatusOK, a.transportView(rec))
}

func (a *API) getTransport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("transport"))
		return
	}
	rec, err := a.store.GetTransport(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("transport"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, rec.Version))
	httpx.JSON(w, http.StatusOK, a.transportView(rec))
}

// resendInterchange queues a new delivery of an outbound interchange whose
// last delivery failed or was rejected (a new AS2 Message-ID or file name).
func (a *API) resendInterchange(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("interchange"))
		return
	}
	d, err := a.store.Resend(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("interchange"))
		return
	}
	if errors.Is(err, store.ErrNotResendable) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNotResendable,
			"only an outbound interchange whose last delivery FAILED or was REJECTED, to a partner with an AS2 or SFTP transport, can be resent"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, d)
}
