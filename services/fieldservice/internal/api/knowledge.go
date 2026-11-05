package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

// Knowledge duties follow the PRD names (8.45.17).
const (
	dutyKbRead    = "fieldservice.knowledge-article.read"
	dutyKbAuthor  = "fieldservice.knowledge-article.author"
	dutyKbPublish = "fieldservice.knowledge-article.publish"
)

const (
	maxArticleText   = 4000
	maxTitle         = 200
	maxQueryText     = 500
	maxApplicability = 50
	defaultLanguage  = "en"
)

var languageTag = regexp.MustCompile(`^[a-z]{2}(-[A-Za-z]{2,4})?$`)

func (a *API) registerKnowledge(r chi.Router) {
	r.With(authz.RequireDuty(dutyKbAuthor)).Post("/v1/service/knowledge/articles", a.createArticle)
	r.With(authz.RequireDuty(dutyKbRead)).Get("/v1/service/knowledge/articles", a.listArticles)
	r.With(authz.RequireDuty(dutyKbRead)).Get("/v1/service/knowledge/articles/search", a.searchArticles)
	r.With(authz.RequireDuty(dutyKbRead)).Get("/v1/service/knowledge/articles/{id}", a.getArticle)
	r.With(authz.RequireDuty(dutyKbAuthor)).Put("/v1/service/knowledge/articles/{id}", a.updateArticle)
	r.With(authz.RequireDuty(dutyKbAuthor)).Post("/v1/service/knowledge/articles/{id}:new-version", a.newArticleVersion)
	r.With(authz.RequireDuty(dutyKbAuthor)).Post("/v1/service/knowledge/articles/{id}:submit", a.submitArticle)
	r.With(authz.RequireDuty(dutyKbPublish)).Post("/v1/service/knowledge/articles/{id}:reject", a.rejectArticle)
	r.With(authz.RequireDuty(dutyKbPublish)).Post("/v1/service/knowledge/articles/{id}:publish", a.publishArticle)
	r.With(authz.RequireDuty(dutyKbPublish)).Post("/v1/service/knowledge/articles/{id}:retire", a.retireArticle)
	r.With(authz.RequireDuty(dutyKbRead)).Post("/v1/service/knowledge/articles/{id}:vote", a.voteArticle)
}

type articleRequest struct {
	Title         string               `json:"title"`
	Language      string               `json:"language"`
	Symptom       string               `json:"symptom"`
	Cause         string               `json:"cause"`
	Solution      string               `json:"solution"`
	Applicability domain.Applicability `json:"applicability"`
}

func (req articleRequest) content() store.ArticleContent {
	return store.ArticleContent{Title: strings.TrimSpace(req.Title), Language: req.Language, Symptom: req.Symptom, Cause: req.Cause,
		Solution: req.Solution, Applicability: req.Applicability.Normalized()}
}

// publishView carries KB-PUB-002 when publishing retired an earlier version.
type publishView struct {
	store.PublishResult
	Warnings []string `json:"warnings"`
}

// actorOf returns the calling user's id, which must be a UUID to be recorded as
// author, reviewer or voter; otherwise it writes a 403 and returns false.
func actorOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, _ := tenant.FromContext(r.Context())
	if !ids.Valid(p.UserID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller must be a user with a UUID id"))
		return "", false
	}
	return p.UserID, true
}

func (a *API) createArticle(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req articleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := articleFields(req); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	author, ok := actorOf(w, r)
	if !ok {
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	art, replayed, err := a.store.CreateArticle(r.Context(), req.content(), author, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create article", err)
		return
	}
	setETag(w, art.Revision)
	a.respond(w, replayed, http.StatusCreated, art)
}

// updateArticle replaces the content of a DRAFT version. If-Match carries the revision.
func (a *API) updateArticle(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeArticleNotFound, "knowledge article not found")
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req articleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := articleFields(req)
	delete(fields, "language") // a version's language is fixed; an update need not repeat it
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	art, err := a.store.UpdateArticle(r.Context(), id, rev, req.content())
	if err != nil {
		a.fail(w, r, "update article", err)
		return
	}
	setETag(w, art.Revision)
	httpx.JSON(w, http.StatusOK, art)
}

func (a *API) getArticle(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeArticleNotFound, "knowledge article not found")
	if !ok {
		return
	}
	art, err := a.store.GetArticle(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get article", err)
		return
	}
	setETag(w, art.Revision)
	httpx.JSON(w, http.StatusOK, art)
}

// listArticles lists article versions newest first, by status, language or number.
func (a *API) listArticles(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if v := q.Get("status"); v != "" && !oneOf(v, domain.ArticleStatuses...) {
		fields["status"] = "must be one of " + strings.Join(domain.ArticleStatuses, ", ")
	}
	if v := q.Get("lang"); v != "" && !languageTag.MatchString(v) {
		fields["lang"] = "must be a language tag such as en or fr-CA"
	}
	if len(q.Get("number")) > 40 {
		fields["number"] = "must be an article number such as KB-000001"
	}
	cursor, limit := pageParams(fields, q.Get("cursor"), q.Get("limit"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListArticles(r.Context(), store.ArticleFilter{Status: q.Get("status"), Language: q.Get("lang"),
		Number: q.Get("number"), Before: cursor, Limit: limit})
	if err != nil {
		a.fail(w, r, "list articles", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// newArticleVersion starts the next DRAFT version with the caller as author (BR-45.11).
func (a *API) newArticleVersion(w http.ResponseWriter, r *http.Request) {
	a.articleMove(w, r, "new article version", http.StatusCreated, func(ctx context.Context, id string, rev int, actor string) (store.Article, error) {
		return a.store.NewArticleVersion(ctx, id, rev, actor)
	})
}

func (a *API) submitArticle(w http.ResponseWriter, r *http.Request) {
	a.articleMove(w, r, "submit article", http.StatusOK, func(ctx context.Context, id string, rev int, _ string) (store.Article, error) {
		return a.store.SubmitArticle(ctx, id, rev)
	})
}

func (a *API) rejectArticle(w http.ResponseWriter, r *http.Request) {
	a.articleMove(w, r, "reject article", http.StatusOK, func(ctx context.Context, id string, rev int, _ string) (store.Article, error) {
		return a.store.RejectArticle(ctx, id, rev)
	})
}

func (a *API) retireArticle(w http.ResponseWriter, r *http.Request) {
	a.articleMove(w, r, "retire article", http.StatusOK, func(ctx context.Context, id string, rev int, _ string) (store.Article, error) {
		return a.store.RetireArticle(ctx, id, rev)
	})
}

// articleMove runs a lifecycle action that takes the article id, the If-Match
// revision and the calling user.
func (a *API) articleMove(w http.ResponseWriter, r *http.Request, op string, status int,
	act func(ctx context.Context, id string, rev int, actor string) (store.Article, error)) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeArticleNotFound, "knowledge article not found")
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	actor, ok := actorOf(w, r)
	if !ok {
		return
	}
	art, err := act(r.Context(), id, rev, actor)
	if err != nil {
		a.fail(w, r, op, err)
		return
	}
	setETag(w, art.Revision)
	httpx.JSON(w, status, art)
}

// publishArticle publishes an IN_REVIEW version with the caller as reviewer,
// who must differ from the author (KB-PUB-001); an earlier PUBLISHED version
// is retired and reported as KB-PUB-002.
func (a *API) publishArticle(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeArticleNotFound, "knowledge article not found")
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	reviewer, ok := actorOf(w, r)
	if !ok {
		return
	}
	res, err := a.store.PublishArticle(r.Context(), id, rev, reviewer)
	if err != nil {
		a.fail(w, r, "publish article", err)
		return
	}
	out := publishView{PublishResult: res, Warnings: []string{}}
	if res.RetiredVersion != nil {
		out.Warnings = append(out.Warnings, "KB-PUB-002: publishing retires version "+strconv.Itoa(*res.RetiredVersion))
	}
	setETag(w, res.Article.Revision)
	httpx.JSON(w, http.StatusOK, out)
}

type voteRequest struct {
	Helpful *bool `json:"helpful"`
}

// voteArticle records the caller's helpful or not-helpful vote on a PUBLISHED
// version; voting again replaces the caller's earlier vote.
func (a *API) voteArticle(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeArticleNotFound, "knowledge article not found")
	if !ok {
		return
	}
	var req voteRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Helpful == nil {
		validationFailed(w, r, map[string]string{"helpful": "is required: true or false"})
		return
	}
	voter, ok := actorOf(w, r)
	if !ok {
		return
	}
	art, err := a.store.VoteArticle(r.Context(), id, voter, *req.Helpful)
	if err != nil {
		a.fail(w, r, "vote article", err)
		return
	}
	setETag(w, art.Revision)
	httpx.JSON(w, http.StatusOK, art)
}

// searchArticles searches and suggests PUBLISHED articles for a case
// description and its item, incident type and asset type (FR-45.13, BR-45.11).
func (a *API) searchArticles(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	text := strings.TrimSpace(q.Get("q"))
	if len([]rune(text)) > maxQueryText {
		fields["q"] = fmt.Sprintf("must be at most %d characters", maxQueryText)
	}
	lang := q.Get("lang")
	if lang == "" {
		lang = defaultLanguage
	}
	if !languageTag.MatchString(lang) {
		fields["lang"] = "must be a language tag such as en or fr-CA"
	}
	for _, name := range []string{"itemId", "incidentTypeId", "assetTypeId"} {
		if v := q.Get(name); v != "" {
			uuidField(fields, name, v)
		}
	}
	limit := domain.DefaultSuggestionCount
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > domain.MaxSuggestionCount {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", domain.MaxSuggestionCount)
		}
		limit = n
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	items, err := a.store.SuggestArticles(r.Context(), store.SuggestQuery{
		Text: text, ItemID: q.Get("itemId"), IncidentTypeID: q.Get("incidentTypeId"),
		AssetTypeID: q.Get("assetTypeId"), Language: lang, Limit: limit,
	})
	if err != nil {
		a.fail(w, r, "search articles", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// articleFields validates the editable article content.
func articleFields(req articleRequest) map[string]string {
	fields := map[string]string{}
	if n := len([]rune(strings.TrimSpace(req.Title))); n < 1 || n > maxTitle {
		fields["title"] = fmt.Sprintf("must be 1 to %d characters", maxTitle)
	}
	if !languageTag.MatchString(req.Language) {
		fields["language"] = "must be a language tag such as en or fr-CA"
	}
	for name, v := range map[string]string{"symptom": req.Symptom, "cause": req.Cause, "solution": req.Solution} {
		if len([]rune(v)) > maxArticleText {
			fields[name] = fmt.Sprintf("must be at most %d characters", maxArticleText)
		}
	}
	if strings.TrimSpace(req.Solution) == "" {
		fields["solution"] = "is required"
	}
	uuidList(fields, "applicability.itemIds", req.Applicability.ItemIDs)
	uuidList(fields, "applicability.itemGroupIds", req.Applicability.ItemGroupIDs)
	uuidList(fields, "applicability.incidentTypeIds", req.Applicability.IncidentTypeIDs)
	uuidList(fields, "applicability.assetTypeIds", req.Applicability.AssetTypeIDs)
	uuidList(fields, "applicability.categoryIds", req.Applicability.CategoryIDs)
	return fields
}

// uuidList validates a bounded list of UUIDs.
func uuidList(fields map[string]string, name string, list []string) {
	if len(list) > maxApplicability {
		fields[name] = fmt.Sprintf("must hold at most %d entries", maxApplicability)
		return
	}
	for _, v := range list {
		if !ids.Valid(v) {
			fields[name] = "every entry must be a UUID"
			return
		}
	}
}
