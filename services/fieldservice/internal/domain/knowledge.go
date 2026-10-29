package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Knowledge article lifecycle (FR-45.13, BR-45.11): DRAFT → IN_REVIEW →
// PUBLISHED → RETIRED, with a reviewer sending IN_REVIEW back to DRAFT.
const (
	ArticleDraft     = "DRAFT"
	ArticleInReview  = "IN_REVIEW"
	ArticlePublished = "PUBLISHED"
	ArticleRetired   = "RETIRED"
)

// ArticleStatuses lists the article statuses.
var ArticleStatuses = []string{ArticleDraft, ArticleInReview, ArticlePublished, ArticleRetired}

// Suggestion parameters (BR-45.11, PRD 8.45.13 "Suggestion count and weights").
const (
	DefaultSuggestionCount = 5
	MaxSuggestionCount     = 20

	WeightText         = 0.60
	WeightItem         = 0.25
	WeightIncidentType = 0.10
	WeightAssetType    = 0.05
)

// MinHelpfulVotes is the vote count from which the helpful share is shown (BR-45.11).
const MinHelpfulVotes = 10

// Applicability says where an article applies (FR-45.13).
type Applicability struct {
	ItemIDs         []string `json:"itemIds"`
	ItemGroupIDs    []string `json:"itemGroupIds"`
	IncidentTypeIDs []string `json:"incidentTypeIds"`
	AssetTypeIDs    []string `json:"assetTypeIds"`
	CategoryIDs     []string `json:"categoryIds"`
}

// Normalized returns the applicability with every list non-nil, so it is stored
// and published with empty arrays rather than nulls.
func (a Applicability) Normalized() Applicability {
	for _, l := range []*[]string{&a.ItemIDs, &a.ItemGroupIDs, &a.IncidentTypeIDs, &a.AssetTypeIDs, &a.CategoryIDs} {
		if *l == nil {
			*l = []string{}
		}
	}
	return a
}

func inState(status, want, action string) error {
	if status != want {
		return &Error{Code: CodeArticleNotInState, Detail: fmt.Sprintf("only a %s article can be %s; this one is %s", want, action, status)}
	}
	return nil
}

// CanEditArticle reports whether an article's content may change: only a DRAFT.
func CanEditArticle(status string) error { return inState(status, ArticleDraft, "edited") }

// CanSubmitArticle reports whether an article may go to review: only a DRAFT.
func CanSubmitArticle(status string) error {
	return inState(status, ArticleDraft, "submitted for review")
}

// CanRejectArticle reports whether a reviewer may send an article back to DRAFT.
func CanRejectArticle(status string) error {
	return inState(status, ArticleInReview, "sent back to draft")
}

// CanRetireArticle reports whether an article may be retired without a successor.
func CanRetireArticle(status string) error { return inState(status, ArticlePublished, "retired") }

// CheckPublish enforces the publish rules: the article must be IN_REVIEW and the
// reviewer must differ from the author (BR-45.11, KB-PUB-001).
func CheckPublish(status, authorID, reviewerID string) error {
	if err := inState(status, ArticleInReview, "published"); err != nil {
		return err
	}
	if reviewerID == "" || reviewerID == authorID {
		return &Error{Code: CodeReviewerSoD, Detail: "the reviewer must differ from the author"}
	}
	return nil
}

// CanNewVersion reports whether a new draft version may start from an article:
// a PUBLISHED or RETIRED version can be revised.
func CanNewVersion(status string) error {
	if status != ArticlePublished && status != ArticleRetired {
		return &Error{Code: CodeArticleNotInState, Detail: "a new version starts from a PUBLISHED or RETIRED article"}
	}
	return nil
}

// Helpfulness returns the helpful share in percent, rounded half-even to one
// place, or nil below MinHelpfulVotes votes (BR-45.11).
func Helpfulness(yes, no int) *decimal.Decimal {
	total := yes + no
	if total < MinHelpfulVotes {
		return nil
	}
	p := decimal.NewFromInt(int64(yes)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(total))).RoundBank(1)
	return &p
}
