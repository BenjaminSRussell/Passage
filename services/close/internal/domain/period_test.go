package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func code(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestDecide_Table(t *testing.T) {
	modules := []domain.Module{domain.ModuleGL, domain.ModuleAP, domain.ModuleAR, domain.ModuleInventory, domain.ModuleAssets}
	groups := []string{"", "controllers", "others"}
	statuses := map[domain.Status]string{
		domain.StatusOpen: "", domain.StatusOnHold: domain.CodePeriodOnHold,
		domain.StatusClosed: domain.CodePeriodClosed, domain.StatusPermanentlyClosed: domain.CodePeriodClosed,
		domain.StatusNeverOpened: domain.CodePeriodClosed,
	}
	for status, want := range statuses {
		for _, m := range modules {
			for _, g := range groups {
				rows := []domain.Row{{Module: m, Status: status, AllowedGroupID: "controllers"}}
				if status == domain.StatusNeverOpened {
					rows = nil
				}
				wantCode := want
				if status == domain.StatusOnHold && g == "controllers" {
					wantCode = ""
				}
				if _, err := domain.Decide(rows, m, g); code(err) != wantCode {
					t.Errorf("status %s module %s group %q: got %q, want %q", status, m, g, code(err), wantCode)
				}
			}
		}
	}
}

func TestDecide_ModuleRowOverridesAll(t *testing.T) {
	rows := []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusClosed}, {Module: domain.ModuleAP, Status: domain.StatusOpen}}
	if _, err := domain.Decide(rows, domain.ModuleAP, ""); err != nil {
		t.Fatalf("module row must win over ALL: %v", err)
	}
	if _, err := domain.Decide(rows, domain.ModuleAR, ""); code(err) != domain.CodePeriodClosed {
		t.Fatalf("ALL row must apply to modules without a row: %v", err)
	}
}

func TestPostingPaths_ClosedRejected(t *testing.T) {
	paths := []string{"manual", "ap", "ar", "inv", "fa", "proj", "tax", "bank", "payroll", "cash", "prod", "reversal", "migration", "batch", "unknown-system", ""}
	closed := []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusClosed}}
	for _, system := range paths {
		if _, err := domain.Decide(closed, domain.ModuleForSource(system), "controllers"); code(err) != domain.CodePeriodClosed {
			t.Errorf("posting path %q was not rejected in a closed period: %v", system, err)
		}
	}
}

func TestPostingPaths_ManualGLAllowedWhenOnlyAPClosed(t *testing.T) {
	rows := []domain.Row{{Module: domain.ModuleAP, Status: domain.StatusClosed}, {Module: domain.ModuleGL, Status: domain.StatusOpen}}
	if _, err := domain.Decide(rows, domain.ModuleForSource("manual"), ""); err != nil {
		t.Fatalf("manual GL posting must pass: %v", err)
	}
	if _, err := domain.Decide(rows, domain.ModuleForSource("ap"), ""); code(err) != domain.CodePeriodClosed {
		t.Fatalf("AP posting must be rejected: %v", err)
	}
}

func TestPostingPaths_UnmappedSourceRejectedWhenAnyModuleClosed(t *testing.T) {
	paths := []string{"manual", "ap", "ar", "inv", "fa", "proj", "tax", "bank", "payroll", "cash", "prod", "reversal", "migration", "batch", "unknown-system", ""}
	rows := []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusOpen}, {Module: domain.ModuleAP, Status: domain.StatusClosed}}
	for _, system := range paths {
		module := domain.ModuleForSource(system)
		_, err := domain.Decide(rows, module, "")
		wantClosed := module == domain.ModuleAP || module == domain.ModuleAll
		if wantClosed != (code(err) == domain.CodePeriodClosed) {
			t.Errorf("posting path %q (module %s): got %v", system, module, err)
		}
	}
}

func TestValidateTransition_Table(t *testing.T) {
	apOpen := []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusOpen}, {Module: domain.ModuleAP, Status: domain.StatusOpen}}
	apClosed := []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusOpen}, {Module: domain.ModuleAP, Status: domain.StatusClosed}}
	tests := []struct {
		name    string
		rows    []domain.Row
		to      domain.Status
		module  domain.Module
		wantErr string
		wantMsg string
	}{
		{"GL close while AP open", apOpen, domain.StatusClosed, domain.ModuleGL, domain.CodeInvalidTransition, "ap"},
		{"GL close after AP closed", apClosed, domain.StatusClosed, domain.ModuleGL, "", ""},
		{"AP close while GL open", apOpen, domain.StatusClosed, domain.ModuleAP, "", ""},
		{"closed to open without reopen workflow", apClosed, domain.StatusOpen, domain.ModuleAP, domain.CodeInvalidTransition, "reopen"},
		{"permanently closed is final", []domain.Row{{Module: domain.ModuleGL, Status: domain.StatusPermanentlyClosed}}, domain.StatusOpen, domain.ModuleGL, domain.CodeInvalidTransition, "permanently"},
		{"never opened to closed", nil, domain.StatusClosed, domain.ModuleGL, domain.CodeInvalidTransition, "opened first"},
		{"never opened to open", nil, domain.StatusOpen, domain.ModuleGL, "", ""},
		{"open to on hold", apOpen, domain.StatusOnHold, domain.ModuleAP, "", ""},
		{"same status", apOpen, domain.StatusOpen, domain.ModuleAP, domain.CodeInvalidTransition, "already"},
		{"unknown target", apOpen, domain.StatusNeverOpened, domain.ModuleAP, domain.CodeInvalidTransition, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateTransition(tc.rows, tc.to, tc.module)
			if code(err) != tc.wantErr || (tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg)) {
				t.Fatalf("got %v, want code %q containing %q", err, tc.wantErr, tc.wantMsg)
			}
		})
	}
}
