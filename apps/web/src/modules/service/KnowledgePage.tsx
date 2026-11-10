import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, FormField, Input, PageHeader, Select, Skeleton, StatusPill, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  ARTICLE_STATUSES,
  createArticle,
  describeProblem,
  getArticle,
  getServiceObject,
  listArticles,
  moveArticle,
  newKey,
  publishArticle,
  searchArticles,
  updateArticle,
  voteArticle,
  type Applicability,
  type Article,
  type ArticleInput,
  type ArticleMove,
  type ArticleStatus,
  type Problem,
  type Suggestion,
  type Versioned,
} from "./api/client";
import { Details } from "./Details";
import { dash, formatDateTime, sentence, shortId, UUID } from "./format";
import { PagedTable } from "./PagedTable";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

type Errors = Record<string, string>;

const LANGUAGE = /^[a-z]{2}(-[A-Za-z]{2,4})?$/;

/** "90 % of 10 votes", or the vote count below ten votes (BR-45.11). */
export function helpfulText(a: Pick<Article, "helpfulYes" | "helpfulNo" | "helpfulPercent">): string {
  const votes = a.helpfulYes + a.helpfulNo;
  if (a.helpfulPercent !== undefined) return `${a.helpfulPercent} % of ${votes} votes`;
  return votes === 0 ? "No votes yet" : `${votes} ${votes === 1 ? "vote" : "votes"} (shown from 10)`;
}

const articleColumns: DataTableColumn<Article>[] = [
  { key: "number", header: "Article", mono: true, render: (row) => `${row.number} v${row.version}` },
  { key: "title", header: "Title" },
  { key: "language", header: "Language", mono: true },
  { key: "helpful", header: "Helpful", render: helpfulText },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

const suggestionColumns: DataTableColumn<Suggestion>[] = [
  { key: "number", header: "Article", mono: true, render: (row) => row.article.number },
  { key: "title", header: "Title", render: (row) => row.article.title },
  { key: "symptom", header: "Symptom", render: (row) => row.article.symptom || dash },
  { key: "score", header: "Score", mono: true, render: (row) => row.score.toFixed(2) },
];

/** Knowledge base (PRD 8.45.6): article versions by status, and a search over the PUBLISHED ones. */
export function KnowledgePage() {
  const navigate = useNavigate();
  const [status, setStatus] = useState<ArticleStatus | "">("");
  const [text, setText] = useState("");
  const [query, setQuery] = useState("");

  function search(event: FormEvent) {
    event.preventDefault();
    setQuery(text.trim());
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Knowledge base"
        description="Symptom and solution articles. An author drafts and submits; another person reviews and publishes."
        actions={
          <Link className={buttonClassName("primary")} to="/service/knowledge/new">
            New article
          </Link>
        }
      />
      <form className={styles.toolbar} onSubmit={search} role="search" aria-label="Article search">
        <FormField id="knowledge-q" label="Search published articles" hint="Describe the symptom; any matching word counts." optional>
          {(control) => <Input {...control} value={text} onChange={(e) => setText(e.target.value)} />}
        </FormField>
        <div className={styles.actions}>
          <Button type="submit">Search</Button>
          {query ? (
            <Button
              variant="ghost"
              onClick={() => {
                setText("");
                setQuery("");
              }}
            >
              Clear
            </Button>
          ) : null}
        </div>
      </form>
      {query ? (
        <SearchResults text={query} label="Search results" onOpen={(row) => navigate(`/service/knowledge/${row.article.id}`)} />
      ) : (
        <>
          <div className={styles.toolbar}>
            <FormField id="knowledge-status" label="Status">
              {(control) => (
                <Select {...control} value={status} onChange={(e) => setStatus(e.target.value as ArticleStatus | "")}>
                  <option value="">Any status</option>
                  {ARTICLE_STATUSES.map((s) => (
                    <option key={s} value={s}>
                      {sentence(s)}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <PagedTable
            label="Knowledge articles"
            columns={articleColumns}
            load={(cursor, signal) => listArticles({ status, cursor }, signal)}
            loadKey={status}
            empty={{ title: "No articles", description: "Write the first article with its symptom and solution." }}
            onRowOpen={(row) => navigate(`/service/knowledge/${row.id}`)}
          />
        </>
      )}
    </div>
  );
}

/** Ranked PUBLISHED articles for a text and, when known, the item (BR-45.11). */
function SearchResults({ text, itemId, label, onOpen }: { text: string; itemId?: string; label: string; onOpen(row: Suggestion): void }) {
  const state = useLoad((signal) => searchArticles({ q: text, itemId }, signal), `${text}|${itemId ?? ""}`);
  if (state.error) {
    return (
      <Alert tone="danger" title="Could not search the knowledge base" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        <ProblemMessage problem={state.error} />
      </Alert>
    );
  }
  return (
    <DataTable
      label={label}
      columns={suggestionColumns}
      rows={state.data?.items ?? []}
      getRowKey={(row) => row.article.id}
      loading={state.loading}
      empty={{ title: "No matching articles", description: "No published article shares a word with this text or applies to the item." }}
      onRowOpen={onOpen}
    />
  );
}

/**
 * Suggested articles for a case (FR-45.13, AC-45.12): its description ranked
 * against the PUBLISHED articles, with the item of its service object when it has one.
 */
export function ArticleSuggestions({ description, serviceObjectId }: { description: string; serviceObjectId?: string | null }) {
  const navigate = useNavigate();
  // The item weighs 0.25 of the score; a unit that cannot be read only loses that part.
  const item = useLoad(
    async (signal) => (serviceObjectId ? (await getServiceObject(serviceObjectId, signal).catch(() => null))?.value.itemId ?? "" : ""),
    serviceObjectId ?? "",
  );
  const text = description.trim();
  return (
    <Card title="Suggested articles" headingLevel={3}>
      {!text && !serviceObjectId ? (
        <p className={styles.note}>The case has no description or service object to match articles against.</p>
      ) : item.loading ? (
        <Skeleton height="tile" />
      ) : (
        <SearchResults text={text} itemId={item.data || undefined} label="Suggested articles" onOpen={(row) => navigate(`/service/knowledge/${row.article.id}`)} />
      )}
    </Card>
  );
}

export interface ArticleValues {
  title: string;
  language: string;
  symptom: string;
  cause: string;
  solution: string;
  /** Item UUIDs, one per line. */
  itemIds: string;
}

export const emptyArticle = (): ArticleValues => ({ title: "", language: "en", symptom: "", cause: "", solution: "", itemIds: "" });

export function articleValues(a: Article): ArticleValues {
  return { title: a.title, language: a.language, symptom: a.symptom, cause: a.cause, solution: a.solution, itemIds: a.applicability.itemIds.join("\n") };
}

/** Checks that mirror the service (knowledge.go articleFields). keep holds the applicability lists this form does not edit. */
export function validateArticle(values: ArticleValues, keep?: Applicability): { errors: Errors; input?: ArticleInput } {
  const errors: Errors = {};
  const title = values.title.trim();
  if (title === "" || [...title].length > 200) errors.title = "Enter a title of at most 200 characters.";
  if (!LANGUAGE.test(values.language)) errors.language = "Enter a language tag such as en or fr-CA.";
  for (const name of ["symptom", "cause", "solution"] as const) {
    if ([...values[name]].length > 4000) errors[name] = "Keep it to 4,000 characters.";
  }
  if (!values.solution.trim()) errors.solution = "Describe the solution steps.";
  const itemIds = values.itemIds
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
  if (itemIds.length > 50) errors.itemIds = "List at most 50 items.";
  else if (itemIds.some((v) => !UUID.test(v))) errors.itemIds = "Enter one item UUID per line.";
  if (Object.keys(errors).length > 0) return { errors };
  const base: Applicability = keep ?? { itemIds: [], itemGroupIds: [], incidentTypeIds: [], assetTypeIds: [], categoryIds: [] };
  return {
    errors,
    input: { title, language: values.language, symptom: values.symptom, cause: values.cause, solution: values.solution, applicability: { ...base, itemIds } },
  };
}

function ArticleFields({ values, errors, onChange, languageFixed = false }: { values: ArticleValues; errors: Errors; onChange(patch: Partial<ArticleValues>): void; languageFixed?: boolean }) {
  const area = (name: "symptom" | "cause" | "solution" | "itemIds", label: string, hint?: string, optional = false) => {
    const error = errors[name] ?? (name === "itemIds" ? errors["applicability.itemIds"] : undefined);
    return (
      <FormField id={`article-${name}`} label={label} hint={hint} error={error} optional={optional}>
        {(control) => (
          <Textarea {...control} className={name === "itemIds" ? styles.mono : undefined} value={values[name]} onChange={(e) => onChange({ [name]: e.target.value })} invalid={Boolean(error)} />
        )}
      </FormField>
    );
  };
  return (
    <>
      <FormField id="article-title" label="Title" error={errors.title}>
        {(control) => <Input {...control} value={values.title} onChange={(e) => onChange({ title: e.target.value })} invalid={Boolean(errors.title)} />}
      </FormField>
      <FormField id="article-language" label="Language" hint={languageFixed ? "Fixed for this version." : "Each language is its own version, such as en or fr-CA."} error={errors.language}>
        {(control) => (
          <Input {...control} className={styles.mono} value={values.language} disabled={languageFixed} onChange={(e) => onChange({ language: e.target.value })} invalid={Boolean(errors.language)} />
        )}
      </FormField>
      {area("symptom", "Symptom", "What the customer or technician sees.", true)}
      {area("cause", "Cause", undefined, true)}
      {area("solution", "Solution", "The steps that fix it.")}
      {area("itemIds", "Applies to items", "Item UUIDs, one per line. A case on one of these items ranks the article higher.", true)}
    </>
  );
}

function FormFailure({ failure, title }: { failure: Problem | null; title: string }) {
  if (!failure) return null;
  return (
    <div className={styles.formActions}>
      <Alert tone="danger" title={title} live="alert">
        <ProblemMessage problem={failure} />
      </Alert>
    </div>
  );
}

/** Write a new article; it starts as version 1 in DRAFT with the caller as author. */
export function NewArticlePage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<ArticleValues>(emptyArticle());
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newKey());

  function update(patch: Partial<ArticleValues>) {
    setValues((v) => ({ ...v, ...patch }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateArticle(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      const created = await createArticle(input, key.current);
      navigate(`/service/knowledge/${created.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "idempotency_key_reuse") key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New knowledge article" description="Saved as a draft. Submit it for review when it is ready; another person publishes it." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New knowledge article">
          <ArticleFields values={values} errors={errors} onChange={update} />
          <FormFailure failure={failure} title="Could not save the article" />
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save draft
            </Button>
            <Link className={buttonClassName("ghost")} to="/service/knowledge">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

/** What each lifecycle action is called and what it says when done. */
const moves: Record<ArticleMove | "publish", { label: string; done: string }> = {
  submit: { label: "Submit for review", done: "Submitted for review." },
  reject: { label: "Send back to draft", done: "Sent back to the author as a draft." },
  publish: { label: "Publish", done: "Published." },
  retire: { label: "Retire", done: "Retired; it no longer appears in suggestions." },
  "new-version": { label: "New version", done: "" },
};

/** The actions each status offers (BR-45.11). */
export const ARTICLE_ACTIONS: Record<ArticleStatus, (ArticleMove | "publish")[]> = {
  DRAFT: ["submit"],
  IN_REVIEW: ["publish", "reject"],
  PUBLISHED: ["new-version", "retire"],
  RETIRED: ["new-version"],
};

const versionColumns: DataTableColumn<Article>[] = [
  { key: "version", header: "Version", mono: true, render: (row) => `v${row.version}` },
  { key: "language", header: "Language", mono: true },
  { key: "publishedAt", header: "Published", mono: true, render: (row) => formatDateTime(row.publishedAt) },
  { key: "status", header: "Status", render: (row) => <StatusPill status={row.status} /> },
];

/** One article version: its content, review and publishing, retiring, new versions and helpfulness votes. */
export function ArticleDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getArticle(id, signal), id);
  const [latest, setLatest] = useState<Versioned<Article> | null>(null);
  const [editing, setEditing] = useState<ArticleValues | null>(null);
  const [errors, setErrors] = useState<Errors>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState("");
  const [confirmRetire, setConfirmRetire] = useState(false);
  const current = latest && latest.value.id === id ? latest : state.data;

  if (state.error && !current) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Knowledge article" />
        <Alert tone="danger" title="Could not load the article" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!current) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Knowledge article" />
        <span className="visually-hidden">Loading the article…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const { value: art, etag } = current;

  async function run(action: string, call: () => Promise<Versioned<Article>>, done: string | (() => string)) {
    setBusy(action);
    setFailure(null);
    setNotice("");
    try {
      const next = await call();
      setLatest(next);
      setEditing(null);
      setNotice(typeof done === "string" ? done : done());
      if (next.value.id !== id) navigate(`/service/knowledge/${next.value.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
      if (problem.code === "conflict_version") state.reload();
    } finally {
      setBusy(null);
    }
  }

  function act(move: ArticleMove | "publish") {
    if (move === "retire") {
      setConfirmRetire(true);
      return;
    }
    if (move === "publish") {
      let warnings = "";
      void run(
        "publish",
        async () => {
          const res = await publishArticle(art.id, etag);
          // KB-PUB-002: publishing retired the earlier version of this language.
          warnings = res.value.warnings.join(" ");
          return { value: res.value.article, etag: res.etag };
        },
        () => (warnings ? `Published. ${warnings}` : "Published."),
      );
      return;
    }
    void run(move, () => moveArticle(art.id, etag, move), move === "new-version" ? "Version started as a draft." : moves[move].done);
  }

  function save(event: FormEvent) {
    event.preventDefault();
    if (!editing) return;
    const { errors: found, input } = validateArticle(editing, art.applicability);
    setErrors(found);
    if (!input) return;
    void run("save", () => updateArticle(art.id, etag, input), "Draft saved.");
  }

  const failureTitle =
    failure?.code === "KB-PUB-001"
      ? "You wrote this version; another reviewer must publish it"
      : failure?.code === "KB-ART-003"
        ? "Another version of this article is already in draft or review"
        : failure?.code === "conflict_version"
          ? "Someone else changed this article; it was reloaded"
          : "The action failed";

  return (
    <div className={styles.stack}>
      <PageHeader
        title={art.title}
        description={`${art.number}, version ${art.version}, ${art.language}`}
        meta={<StatusPill status={art.status} />}
        actions={
          <div className={styles.actions}>
            {art.status === "DRAFT" && !editing ? (
              <Button
                onClick={() => {
                  setErrors({});
                  setEditing(articleValues(art));
                }}
              >
                Edit draft
              </Button>
            ) : null}
            {ARTICLE_ACTIONS[art.status].map((move) => (
              <Button key={move} variant={move === "retire" ? "danger" : move === "publish" || move === "submit" ? "primary" : "secondary"} loading={busy === move} onClick={() => act(move)}>
                {moves[move].label}
              </Button>
            ))}
          </div>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title={failureTitle} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {editing ? (
        <Card title="Edit draft" headingLevel={3}>
          <form className={styles.form} onSubmit={save} noValidate aria-label="Edit draft">
            <ArticleFields values={editing} errors={errors} onChange={(patch) => setEditing({ ...editing, ...patch })} languageFixed />
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy === "save"}>
                Save draft
              </Button>
              <Button variant="ghost" onClick={() => setEditing(null)}>
                Discard changes
              </Button>
            </div>
          </form>
        </Card>
      ) : (
        <Card title="Article" headingLevel={3}>
          <Details
            label="Article"
            items={[
              ["Symptom", art.symptom || dash],
              ["Cause", art.cause || dash],
              ["Solution", art.solution],
              ["Applies to items", art.applicability.itemIds.length > 0 ? art.applicability.itemIds.map(shortId).join(", ") : "Any item"],
            ]}
          />
        </Card>
      )}
      <Card title="Review" headingLevel={3}>
        <Details
          label="Review"
          items={[
            ["Author", art.authorId, true],
            ["Reviewer", art.reviewerId ?? dash, true],
            ["Published", formatDateTime(art.publishedAt), true],
            ["Helpful", helpfulText(art)],
          ]}
        />
      </Card>
      {art.status === "PUBLISHED" ? (
        <Card title="Was this article helpful?" headingLevel={3}>
          <div className={styles.actions}>
            <Button loading={busy === "yes"} onClick={() => run("yes", () => voteArticle(art.id, true), "Thanks; your vote counts as helpful.")}>
              Helpful
            </Button>
            <Button loading={busy === "no"} onClick={() => run("no", () => voteArticle(art.id, false), "Thanks; your vote counts as not helpful.")}>
              Not helpful
            </Button>
          </div>
          <p className={styles.note}>
            <Badge tone="neutral">One vote each</Badge> Voting again replaces your earlier vote.
          </p>
        </Card>
      ) : null}
      <Card title="Versions" headingLevel={3}>
        <PagedTable
          label="Article versions"
          columns={versionColumns}
          load={(cursor, signal) => listArticles({ number: art.number, cursor }, signal)}
          loadKey={`${art.number}|${art.revision}|${art.id}`}
          empty={{ title: "No versions", description: "This article has no other versions." }}
          onRowOpen={(row) => navigate(`/service/knowledge/${row.id}`)}
        />
      </Card>
      {confirmRetire ? (
        <ConfirmDialog
          title="Retire this article?"
          description="It leaves the suggestions and search. Start a new version to bring it back."
          confirmLabel="Retire"
          cancelLabel="Keep it published"
          onConfirm={() => {
            setConfirmRetire(false);
            void run("retire", () => moveArticle(art.id, etag, "retire"), moves.retire.done);
          }}
          onCancel={() => setConfirmRetire(false)}
        />
      ) : null}
    </div>
  );
}
