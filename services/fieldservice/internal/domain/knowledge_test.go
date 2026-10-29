package domain

import (
	"errors"
	"testing"
)

// AC-45.12 and KB-PUB-001: publish requires IN_REVIEW and a reviewer other than the author.
func TestCheckPublish_RulesAndSegregation(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		author   string
		reviewer string
		wantCode string
	}{
		{"reviewer differs", ArticleInReview, "u-author", "u-reviewer", ""},
		{"reviewer equals author", ArticleInReview, "u-author", "u-author", CodeReviewerSoD},
		{"no reviewer", ArticleInReview, "u-author", "", CodeReviewerSoD},
		{"draft not in review", ArticleDraft, "u-author", "u-reviewer", CodeArticleNotInState},
		{"already published", ArticlePublished, "u-author", "u-reviewer", CodeArticleNotInState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckPublish(tc.status, tc.author, tc.reviewer)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != tc.wantCode {
				t.Fatalf("err = %v, want code %s", err, tc.wantCode)
			}
		})
	}
}

// BR-45.11 lifecycle: each move is allowed from exactly one status (new
// versions from PUBLISHED or RETIRED), and every refusal carries KB-ART-002.
func TestArticleLifecycleMoves(t *testing.T) {
	moves := map[string]struct {
		check   func(string) error
		allowed []string
	}{
		"edit":        {CanEditArticle, []string{ArticleDraft}},
		"submit":      {CanSubmitArticle, []string{ArticleDraft}},
		"reject":      {CanRejectArticle, []string{ArticleInReview}},
		"retire":      {CanRetireArticle, []string{ArticlePublished}},
		"new version": {CanNewVersion, []string{ArticlePublished, ArticleRetired}},
	}
	for name, m := range moves {
		for _, status := range ArticleStatuses {
			err := m.check(status)
			want := false
			for _, a := range m.allowed {
				want = want || a == status
			}
			if want && err != nil {
				t.Fatalf("%s from %s: unexpected %v", name, status, err)
			}
			var de *Error
			if !want && (!errors.As(err, &de) || de.Code != CodeArticleNotInState) {
				t.Fatalf("%s from %s: err = %v, want %s", name, status, err, CodeArticleNotInState)
			}
		}
	}
}

// BR-45.11: helpfulness is shown only from ten votes, as a decimal percent.
func TestHelpfulness_NeedsTenVotes(t *testing.T) {
	tests := []struct {
		yes, no int
		want    string
	}{
		{9, 0, ""},
		{8, 2, "80"},
		{0, 10, "0"},
		{2, 1 + 8, "18.2"},
	}
	for _, tc := range tests {
		got := Helpfulness(tc.yes, tc.no)
		if tc.want == "" {
			if got != nil {
				t.Fatalf("Helpfulness(%d,%d) = %s, want hidden", tc.yes, tc.no, got)
			}
			continue
		}
		if got == nil || got.String() != tc.want {
			t.Fatalf("Helpfulness(%d,%d) = %v, want %s", tc.yes, tc.no, got, tc.want)
		}
	}
}

func TestApplicabilityNormalized_NoNullLists(t *testing.T) {
	a := Applicability{ItemIDs: []string{"i"}}.Normalized()
	if a.ItemIDs[0] != "i" || a.ItemGroupIDs == nil || a.IncidentTypeIDs == nil || a.AssetTypeIDs == nil || a.CategoryIDs == nil {
		t.Fatalf("normalized = %+v", a)
	}
}
