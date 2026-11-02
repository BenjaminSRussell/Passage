package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	CodeNoConnection        = "MIG-NO-CONNECTION"
	CodeConnectionAmbiguous = "MIG-CONNECTION-AMBIGUOUS"
	CodeNoBinding           = "MIG-NO-BINDING"
	CodeExtractFailed       = "MIG-EXTRACT-FAILED"
	CodeTargetUnknown       = "MIG-MAP-TARGET-UNKNOWN"

	maxPage = 200
)

// TargetRun is what the loader of one run needs to know: where rows come from and which legal entity they load into.
type TargetRun struct {
	SourceSystem  string
	Entity        string
	LegalEntityID string
}

// TargetFactory builds the loader (and, where the owning services open a read route, the reader) of one run.
type TargetFactory func(ctx context.Context, run TargetRun) (domain.TargetLoader, domain.TargetReader, error)

// WithTargets turns on live runs (integration track S3): when the extraction environment is also set, a run reads
// its rows from the entity binding of the run's connection profile and loads them through targets. Without it the
// BackendFactory given to New decides, and the default answers 501 MIG-BACKEND-NOT-CONFIGURED.
func (a *API) WithTargets(t TargetFactory) *API {
	a.targets = t
	return a
}

func (a *API) live() bool { return a.targets != nil && a.env != nil }

// backend picks the extractor, loader and reader of a run.
func (a *API) backend(ctx context.Context, run store.Run) (Backend, error) {
	if !a.live() {
		return a.backends(ctx, run.SourceSystem)
	}
	ex, legalEntity, err := a.liveExtractor(ctx, run)
	if err != nil {
		return Backend{}, err
	}
	loader, reader, err := a.targets(ctx, TargetRun{SourceSystem: run.SourceSystem, Entity: run.Entity, LegalEntityID: legalEntity})
	if err != nil {
		return Backend{}, err
	}
	return Backend{Extractor: ex, Loader: loader, Reader: reader}, nil
}

// liveExtractor resolves the binding a run reads: the project's connection profile, or the one active profile of the
// run's source system, and that profile's active binding for the run's entity. It also returns the project's target
// legal entity, if any. Refusals are 4xx problems that say what to set up.
func (a *API) liveExtractor(ctx context.Context, run store.Run) (domain.Extractor, string, error) {
	profileID, legalEntity := "", ""
	if run.ProjectID != "" {
		pr, err := a.store.GetProject(ctx, run.ProjectID)
		if err != nil {
			return nil, "", err
		}
		profileID, legalEntity = pr.ConnectionProfileID, pr.TargetLegalEntityID
	}
	var profile store.ConnectionProfile
	if profileID != "" {
		p, err := a.store.GetConnectionProfile(ctx, profileID)
		if err != nil {
			return nil, "", err
		}
		profile = p
	} else {
		p, err := a.profileFor(ctx, run.SourceSystem)
		if err != nil {
			return nil, "", err
		}
		profile = p
	}
	if profile.Status != domain.ConnActive {
		return nil, "", httpx.NewProblem(http.StatusConflict, CodeProfileDisabled, "the connection profile is disabled")
	}
	if profile.SourceSystem != run.SourceSystem {
		return nil, "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeNoConnection, "the connection profile reads "+profile.SourceSystem+", the run is for "+run.SourceSystem)
	}
	bindings, err := a.store.ListEntityBindings(ctx, store.BindingFilter{ConnectionProfileID: profile.ID, Entity: run.Entity, Status: domain.BindingActive, Limit: 1})
	if err != nil {
		return nil, "", err
	}
	if len(bindings) == 0 {
		return nil, "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeNoBinding, fmt.Sprintf("connection profile %q has no active entity binding for %s", profile.Name, run.Entity))
	}
	b := bindings[0]
	spec := connector.Spec{Kind: profile.Kind, Profile: profile.Config, Source: b.Source, KeyFields: b.KeyFields, CursorField: b.CursorField, CursorType: b.CursorType}
	if needsSecret(profile) && profile.SecretSet {
		if a.keys == nil {
			return nil, "", secretsNotConfigured()
		}
		if spec.Secret, err = a.openSecret(ctx, profile.ID); err != nil {
			return nil, "", err
		}
	}
	env := *a.env
	ex := &connector.Extractor{Bindings: map[string]connector.Binding{run.Entity: {
		Open:        func(context.Context) (connector.Opened, error) { return env.Open(spec) },
		KeyFields:   b.KeyFields,
		CursorField: b.CursorField,
		CursorType:  b.CursorType,
	}}}
	return sourceFailures{ex, spec.Secret}, legalEntity, nil
}

// targetContract is a loader that knows which fields its entities' target APIs take.
type targetContract interface {
	UnknownTargets(entity string, targets []string) []string
}

// unknownTargets refuses a run whose mapping writes a target field the loader would not send anywhere: a misspelt
// optional field would otherwise be dropped on every row while its value still counted in the change hash. A field
// kept only for rules and the hash is named with a leading "_".
func unknownTargets(loader domain.TargetLoader, m domain.Mapping) error {
	c, ok := loader.(targetContract)
	if !ok {
		return nil
	}
	targets := make([]string, len(m.Fields))
	for i, f := range m.Fields {
		targets[i] = f.Target
	}
	unknown := c.UnknownTargets(m.Entity, targets)
	if len(unknown) == 0 {
		return nil
	}
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeTargetUnknown, fmt.Sprintf("%d mapped targets are not fields of the %s target API; rename them, or start a name with _ to keep it for rules and change detection only", len(unknown), m.Entity))
	p.Errors = make(map[string]string, len(unknown))
	for _, t := range unknown {
		p.Errors[t] = "not a field of the " + m.Entity + " target"
	}
	return p
}

// profileFor finds the one active connection profile of a source system.
func (a *API) profileFor(ctx context.Context, sourceSystem string) (store.ConnectionProfile, error) {
	var match []store.ConnectionProfile
	var after *store.ProjectCursor
	for {
		page, err := a.store.ListConnectionProfiles(ctx, store.ConnectionFilter{Status: domain.ConnActive, After: after, Limit: maxPage})
		if err != nil {
			return store.ConnectionProfile{}, err
		}
		for _, p := range page {
			if p.SourceSystem == sourceSystem {
				match = append(match, p)
			}
		}
		if len(page) < maxPage {
			break
		}
		last := page[len(page)-1]
		after = &store.ProjectCursor{Name: last.Name, ID: last.ID}
	}
	switch len(match) {
	case 0:
		return store.ConnectionProfile{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeNoConnection, "no active connection profile reads "+sourceSystem+"; create one, or start the run under a project that names it")
	case 1:
		return match[0], nil
	}
	return store.ConnectionProfile{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeConnectionAmbiguous, "more than one active connection profile reads "+sourceSystem+"; start the run under a project that names one")
}

// openSecret opens a profile's stored secret with the current keys. The plaintext goes to the caller only.
func (a *API) openSecret(ctx context.Context, id string) (string, error) {
	sealed, _, err := a.store.ConnectionSecret(ctx, id)
	if err != nil {
		return "", err
	}
	p, _ := tenant.FromContext(ctx)
	plain, err := a.keys.Open(p.TenantID, secretFieldPrefix+id, sealed)
	if err != nil {
		slog.ErrorContext(ctx, "open connection secret failed", "profileId", id)
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeSecretUnreadable, "the stored secret cannot be opened with the current keys; set it again")
	}
	return string(plain), nil
}

// sourceFailures answers a failed read of the legacy system as a 502 with a classified, scrubbed reason, so the
// operator sees "authentication failed" or "egress denied" instead of an internal error.
type sourceFailures struct {
	domain.Extractor
	secret string
}

func (s sourceFailures) Extract(ctx context.Context, entity string) ([]domain.Record, error) {
	rows, err := s.Extractor.Extract(ctx, entity)
	if err == nil {
		return rows, nil
	}
	return nil, s.classify(err)
}

func (s sourceFailures) classify(err error) error {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return err
	}
	f := probe.Classify(err, s.secret)
	return httpx.NewProblem(http.StatusBadGateway, CodeExtractFailed, fmt.Sprintf("%s: %s", f.Code, f.Message))
}
