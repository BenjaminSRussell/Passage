package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// Problem codes of the knowledge base (listed in the service README).
const (
	CodeArticleNotFound = "KB-ART-001"
	CodeArticleOpen     = "KB-ART-003"
)

// ArticleContent is the editable text and applicability of an article version.
type ArticleContent struct {
	Title         string               `json:"title"`
	Language      string               `json:"language"`
	Symptom       string               `json:"symptom"`
	Cause         string               `json:"cause"`
	Solution      string               `json:"solution"`
	Applicability domain.Applicability `json:"applicability"`
}

// Article is one version of a knowledge article (FR-45.13). Number groups the
// versions; Revision is the optimistic-concurrency token of this version (its ETag).
type Article struct {
	ID     string `json:"id"`
	Number string `json:"number"`
	ArticleContent
	Version     int        `json:"version"`
	Status      string     `json:"status"`
	AuthorID    string     `json:"authorId"`
	ReviewerID  *string    `json:"reviewerId,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	HelpfulYes  int        `json:"helpfulYes"`
	HelpfulNo   int        `json:"helpfulNo"`
	// HelpfulPercent is shown from ten votes (BR-45.11).
	HelpfulPercent *decimal.Decimal `json:"helpfulPercent,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
	Revision       int              `json:"revision"`
}

// PublishResult is the published article and the version it retired, if any (KB-PUB-002).
type PublishResult struct {
	Article        Article `json:"article"`
	RetiredVersion *int    `json:"retiredVersion,omitempty"`
}

// Suggestion is a PUBLISHED article ranked for a case, with its score (BR-45.11).
type Suggestion struct {
	Article Article `json:"article"`
	Score   float64 `json:"score"`
}

// SuggestQuery asks for articles that fit a case description and its applicability.
type SuggestQuery struct {
	Text           string
	ItemID         string
	IncidentTypeID string
	AssetTypeID    string
	Language       string
	Limit          int
}

// ArticleFilter narrows the article list. Empty strings match everything.
type ArticleFilter struct {
	Status   string
	Language string
	Number   string
	Before   string
	Limit    int
}

const articleColumns = `k.id::text, k.number, k.version, k.language, k.title, k.symptom, k.cause, k.solution, k.applicability,
	k.status, k.author_id::text, k.reviewer_id::text, k.published_at, k.created_at, k.revision,
	(select count(*) from service_kb_vote v where v.tenant_id = k.tenant_id and v.article_id = k.id and v.helpful),
	(select count(*) from service_kb_vote v where v.tenant_id = k.tenant_id and v.article_id = k.id and not v.helpful)`

func scanArticleRow(row pgx.Row, extra ...any) (Article, error) {
	var a Article
	var app []byte
	dest := append([]any{&a.ID, &a.Number, &a.Version, &a.Language, &a.Title, &a.Symptom, &a.Cause, &a.Solution, &app,
		&a.Status, &a.AuthorID, &a.ReviewerID, &a.PublishedAt, &a.CreatedAt, &a.Revision, &a.HelpfulYes, &a.HelpfulNo}, extra...)
	if err := row.Scan(dest...); err != nil {
		return a, err
	}
	if err := json.Unmarshal(app, &a.Applicability); err != nil {
		return a, err
	}
	a.Applicability = a.Applicability.Normalized()
	a.HelpfulPercent = domain.Helpfulness(a.HelpfulYes, a.HelpfulNo)
	return a, nil
}

func scanArticle(row pgx.Row) (Article, error) { return scanArticleRow(row) }

// CreateArticle starts a new article, numbered KB-{seq}, at version 1 in DRAFT.
// A replay with the same Idempotency-Key returns the stored article.
func (s *Postgres) CreateArticle(ctx context.Context, in ArticleContent, author, key, requestHash string) (Article, bool, error) {
	tid := tenantOf(ctx)
	var out Article
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_kb_article", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getArticle(ctx, tx, tid, id, false)
			return err
		}
		number, err := nextNumber(ctx, tx, tid, "knowledge", "KB")
		if err != nil {
			return err
		}
		newID := ids.New()
		if err := insertArticle(ctx, tx, tid, newID, number, 1, in, author, nullable(key), nullable(requestHash)); err != nil {
			return err
		}
		out, err = getArticle(ctx, tx, tid, newID, false)
		return err
	})
	return out, replayed, err
}

// UpdateArticle replaces the content of a DRAFT article version. The language
// of a version is fixed when it is created.
func (s *Postgres) UpdateArticle(ctx context.Context, id string, revision int, in ArticleContent) (Article, error) {
	tid := tenantOf(ctx)
	var out Article
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockArticle(ctx, tx, tid, id, revision)
		if err != nil {
			return err
		}
		if err := domain.CanEditArticle(cur.Status); err != nil {
			return ruleProblem(err)
		}
		app, err := json.Marshal(in.Applicability.Normalized())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update service_kb_article set title = $3, symptom = $4, cause = $5, solution = $6, applicability = $7,
			revision = revision + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			tid, id, in.Title, in.Symptom, in.Cause, in.Solution, app); err != nil {
			return err
		}
		out, err = getArticle(ctx, tx, tid, id, false)
		return err
	})
	return out, err
}

// NewArticleVersion starts the next DRAFT version of a PUBLISHED or RETIRED
// article, copying its content, with the caller as author (BR-45.11). Only one
// version of an article is in DRAFT or IN_REVIEW at a time (KB-ART-003).
func (s *Postgres) NewArticleVersion(ctx context.Context, id string, revision int, author string) (Article, error) {
	tid := tenantOf(ctx)
	var out Article
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		src, err := lockArticle(ctx, tx, tid, id, revision)
		if err != nil {
			return err
		}
		if err := domain.CanNewVersion(src.Status); err != nil {
			return ruleProblem(err)
		}
		var next int
		if err := tx.QueryRow(ctx, `select max(version) + 1 from service_kb_article where tenant_id = $1 and number = $2`,
			tid, src.Number).Scan(&next); err != nil {
			return err
		}
		newID := ids.New()
		if err := insertArticle(ctx, tx, tid, newID, src.Number, next, src.ArticleContent, author, nil, nil); err != nil {
			return err
		}
		out, err = getArticle(ctx, tx, tid, newID, false)
		return err
	})
	if isUnique(err) {
		return Article{}, unprocessable(CodeArticleOpen, "another version of this article is already in draft or review")
	}
	return out, err
}

// SubmitArticle moves a DRAFT article to IN_REVIEW.
func (s *Postgres) SubmitArticle(ctx context.Context, id string, revision int) (Article, error) {
	return s.moveArticle(ctx, id, revision, domain.CanSubmitArticle, `status = 'IN_REVIEW'`)
}

// RejectArticle sends an IN_REVIEW article back to DRAFT for its author.
func (s *Postgres) RejectArticle(ctx context.Context, id string, revision int) (Article, error) {
	return s.moveArticle(ctx, id, revision, domain.CanRejectArticle, `status = 'DRAFT'`)
}

// RetireArticle retires a PUBLISHED article that has no successor.
func (s *Postgres) RetireArticle(ctx context.Context, id string, revision int) (Article, error) {
	return s.moveArticle(ctx, id, revision, domain.CanRetireArticle, `status = 'RETIRED'`)
}

// PublishArticle publishes an IN_REVIEW article with the caller as reviewer,
// who must differ from the author (KB-PUB-001). The previous PUBLISHED version
// of the same article and language is retired in the same transaction
// (KB-PUB-002), and the publish event is written to the outbox with it.
func (s *Postgres) PublishArticle(ctx context.Context, id string, revision int, reviewer string) (PublishResult, error) {
	tid := tenantOf(ctx)
	var res PublishResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockArticle(ctx, tx, tid, id, revision)
		if err != nil {
			return err
		}
		if err := domain.CheckPublish(cur.Status, cur.AuthorID, reviewer); err != nil {
			return ruleProblem(err)
		}
		// Retire first: the partial unique index allows one PUBLISHED version per language.
		var retiredID *string
		var retiredVersion *int
		var rid string
		var rv int
		err = tx.QueryRow(ctx, `update service_kb_article set status = 'RETIRED', revision = revision + 1, updated_at = now()
			where tenant_id = $1 and number = $2 and language = $3 and status = 'PUBLISHED'
			returning id::text, version`, tid, cur.Number, cur.Language).Scan(&rid, &rv)
		switch {
		case err == nil:
			retiredID, retiredVersion = &rid, &rv
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if _, err := tx.Exec(ctx, `update service_kb_article set status = 'PUBLISHED', reviewer_id = $3, published_at = now(),
			revision = revision + 1, updated_at = now() where tenant_id = $1 and id = $2`, tid, id, reviewer); err != nil {
			return err
		}
		out, err := getArticle(ctx, tx, tid, id, false)
		if err != nil {
			return err
		}
		res = PublishResult{Article: out, RetiredVersion: retiredVersion}
		return enqueue(ctx, tx, "knowledge_article", id, svcevents.ArticlePublished, tid, svcevents.ArticlePublishedData{
			ArticleID: id, Number: out.Number, Version: out.Version, Language: out.Language, Title: out.Title,
			Applicability: out.Applicability, PublishedAt: out.PublishedAt.UTC().Format(time.RFC3339), RetiredArticleID: retiredID,
		})
	})
	return res, err
}

// VoteArticle records the caller's helpfulness vote on a PUBLISHED article
// (BR-45.11). Each user has one vote per article version; voting again replaces it.
func (s *Postgres) VoteArticle(ctx context.Context, id, voter string, helpful bool) (Article, error) {
	tid := tenantOf(ctx)
	var out Article
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getArticle(ctx, tx, tid, id, false)
		if err != nil {
			return err
		}
		if cur.Status != domain.ArticlePublished {
			return unprocessable(domain.CodeArticleNotInState, "only a PUBLISHED article takes helpfulness votes")
		}
		if _, err := tx.Exec(ctx, `insert into service_kb_vote (tenant_id, article_id, voter_id, helpful) values ($1, $2, $3, $4)
			on conflict (tenant_id, article_id, voter_id) do update set helpful = excluded.helpful, updated_at = now()`,
			tid, id, voter, helpful); err != nil {
			return err
		}
		out, err = getArticle(ctx, tx, tid, id, false)
		return err
	})
	return out, err
}

// GetArticle loads one article version; a missing one is a 404.
func (s *Postgres) GetArticle(ctx context.Context, id string) (Article, error) {
	tid := tenantOf(ctx)
	var a Article
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		a, err = getArticle(ctx, tx, tid, id, false)
		return err
	})
	return a, err
}

// ListArticles returns article versions newest first.
func (s *Postgres) ListArticles(ctx context.Context, f ArticleFilter) (Page[Article], error) {
	return listPage(ctx, s, f.Limit, func(a Article) string { return a.ID }, scanArticle,
		`select `+articleColumns+` from service_kb_article k
		where k.tenant_id = $1 and ($2 = '' or k.status = $2) and ($3 = '' or k.language = $3) and ($4 = '' or k.number = $4)
			and `+keyset("k", "service_kb_article", 5)+`
		order by k.created_at desc, k.id desc limit $6`,
		tenantOf(ctx), f.Status, f.Language, f.Number, f.Before)
}

// SuggestArticles ranks the PUBLISHED articles of one language for a case
// (BR-45.11): ts_rank of the text against the article, normalized to the best
// candidate and weighted 0.6, plus 0.25 for an item match, 0.10 for an incident
// type match and 0.05 for an asset type match. The text matches on any of its
// words, because a case description holds words no article has; an article is
// a candidate when it shares a word with the text or matches the item, incident
// type or asset type. Without text or applicability every PUBLISHED article is.
func (s *Postgres) SuggestArticles(ctx context.Context, q SuggestQuery) ([]Suggestion, error) {
	tid := tenantOf(ctx)
	out := []Suggestion{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with words as (
				-- plainto_tsquery joins the words with AND; any word is enough here.
				select case when $2 = '' then null else replace(plainto_tsquery('simple', $2)::text, ' & ', ' | ')::tsquery end as q
			), ranked as (
				select k.id,
					coalesce(ts_rank(k.search, w.q)::float8, 0) * (case when k.search @@ w.q then 1 else 0 end) as rank,
					($8 <> '' and coalesce(k.applicability->'itemIds', '[]'::jsonb) @> jsonb_build_array($8::text)) as item_match,
					($9 <> '' and coalesce(k.applicability->'incidentTypeIds', '[]'::jsonb) @> jsonb_build_array($9::text)) as incident_match,
					($10 <> '' and coalesce(k.applicability->'assetTypeIds', '[]'::jsonb) @> jsonb_build_array($10::text)) as asset_match
				from service_kb_article k cross join words w
				where k.tenant_id = $1 and k.status = 'PUBLISHED' and k.language = $3
			), scored as (
				select r.*, coalesce(r.rank / nullif(max(r.rank) over (), 0), 0) as text_score from ranked r
				where r.rank > 0 or r.item_match or r.incident_match or r.asset_match
					or ($2 = '' and $8 = '' and $9 = '' and $10 = '')
			)
			select `+articleColumns+`,
				round((s.text_score * $4::float8
					+ case when s.item_match then $5::float8 else 0 end
					+ case when s.incident_match then $6::float8 else 0 end
					+ case when s.asset_match then $7::float8 else 0 end
				)::numeric, 4)::float8 as score
			from scored s join service_kb_article k on k.tenant_id = $1 and k.id = s.id
			order by score desc, k.number
			limit $11`,
			tid, q.Text, q.Language, domain.WeightText, domain.WeightItem, domain.WeightIncidentType, domain.WeightAssetType,
			q.ItemID, q.IncidentTypeID, q.AssetTypeID, q.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sg Suggestion
			if sg.Article, err = scanArticleRow(rows, &sg.Score); err != nil {
				return err
			}
			out = append(out, sg)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) moveArticle(ctx context.Context, id string, revision int, check func(string) error, set string) (Article, error) {
	tid := tenantOf(ctx)
	var out Article
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockArticle(ctx, tx, tid, id, revision)
		if err != nil {
			return err
		}
		if err := check(cur.Status); err != nil {
			return ruleProblem(err)
		}
		if _, err := tx.Exec(ctx, `update service_kb_article set `+set+`, revision = revision + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tid, id); err != nil {
			return err
		}
		out, err = getArticle(ctx, tx, tid, id, false)
		return err
	})
	return out, err
}

func insertArticle(ctx context.Context, tx pgx.Tx, tid, id, number string, version int, c ArticleContent, author string, key, hash *string) error {
	app, err := json.Marshal(c.Applicability.Normalized())
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into service_kb_article (tenant_id, id, number, version, language, title, symptom,
		cause, solution, applicability, status, author_id, idempotency_key, request_hash)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'DRAFT', $11, $12, $13)`,
		tid, id, number, version, c.Language, c.Title, c.Symptom, c.Cause, c.Solution, app, author, key, hash)
	return err
}

// lockArticle loads an article for a command, locked, and checks the caller read its current revision.
func lockArticle(ctx context.Context, tx pgx.Tx, tenantID, id string, revision int) (Article, error) {
	a, err := getArticle(ctx, tx, tenantID, id, true)
	if err != nil {
		return a, err
	}
	if a.Revision != revision {
		return a, versionConflict(a.Revision)
	}
	return a, nil
}

func getArticle(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Article, error) {
	q := `select ` + articleColumns + ` from service_kb_article k where k.tenant_id = $1 and k.id = $2`
	if lock {
		q += ` for update of k`
	}
	a, err := scanArticle(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, httpx.NewProblem(http.StatusNotFound, CodeArticleNotFound, "knowledge article not found")
	}
	return a, err
}
