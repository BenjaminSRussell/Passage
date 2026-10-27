import { useEffect, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  DataTable,
  EmptyState,
  FormField,
  PageHeader,
  Select,
  Skeleton,
  StatGroup,
  StatTile,
  type DataTableColumn,
} from "../../shell/ui";
import {
  describeProblem,
  getProject,
  listProjects,
  nextStages,
  PROJECT_TYPES,
  STAGES,
  transitionProject,
  type Problem,
  type Project,
  type ProjectType,
  type Stage,
} from "./api/client";
import { dash, stageLabel, stageTone, typeLabel } from "./labels";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** The largest page the service returns; tiles show "100+" beyond it. */
const TILE_LIMIT = 100;

export function StageBadge({ stage }: { stage: Stage }) {
  return <Badge tone={stageTone[stage]}>{stageLabel[stage]}</Badge>;
}

interface StageCount {
  stage: Stage;
  count: number;
  more: boolean;
}

/** Projects workspace (PRD §8.41.6): projects by stage, with links to the list and to project entry. */
export function ProjectsHome() {
  const state = useLoad(
    (signal) =>
      Promise.all(
        STAGES.map(async (stage): Promise<StageCount> => {
          const page = await listProjects({ stage, limit: TILE_LIMIT }, signal);
          return { stage, count: page.items.length, more: Boolean(page.nextCursor) };
        }),
      ),
    "home",
  );
  const actions = (
    <>
      <Link className={buttonClassName("secondary")} to="/projects/list">
        All projects
      </Link>
      <Link className={buttonClassName("primary")} to="/projects/new">
        New project
      </Link>
    </>
  );
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Projects"
        description="Project master and stages for your legal entities. Billing, estimates and timesheets arrive in later releases of this workspace."
        actions={actions}
      />
      {state.error ? (
        <Alert tone="danger" title="Could not load projects" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : state.loading || !state.data ? (
        <div className={styles.stack} aria-busy="true">
          <span className="visually-hidden">Loading projects…</span>
          <Skeleton height="tile" />
        </div>
      ) : state.data.every((s) => s.count === 0) ? (
        <Card>
          <EmptyState
            icon="grid"
            title="No projects yet"
            description="Create a project to plan work, record time and costs, and bill the customer."
            action={
              <Link className={buttonClassName("primary")} to="/projects/new">
                New project
              </Link>
            }
          />
        </Card>
      ) : (
        <StatGroup label="Project tiles">
          {state.data.map((s) => (
            <StatTile
              key={s.stage}
              label={stageLabel[s.stage]}
              value={s.more ? `${s.count}+` : s.count}
              tone={s.stage === "ON_HOLD" ? "warning" : "neutral"}
            />
          ))}
        </StatGroup>
      )}
    </div>
  );
}

const columns: DataTableColumn<Project>[] = [
  { key: "id", header: "Project", mono: true },
  { key: "projectType", header: "Type", render: (row) => typeLabel[row.projectType] },
  { key: "stage", header: "Stage", render: (row) => <StageBadge stage={row.stage} /> },
  { key: "projectGroupId", header: "Group", mono: true },
  { key: "parentProjectId", header: "Parent", mono: true, render: (row) => row.parentProjectId ?? dash },
  { key: "currency", header: "Currency", mono: true },
  { key: "startDate", header: "Start", mono: true },
  { key: "endDate", header: "End", mono: true, render: (row) => row.endDate ?? dash },
];

/** Projects list inquiry (PRD §8.41.14), filtered by stage and type, with "Load more" over the service cursor. */
export function ProjectsListPage() {
  const navigate = useNavigate();
  const [stage, setStage] = useState<Stage | "">("");
  const [projectType, setProjectType] = useState<ProjectType | "">("");
  const state = useLoad((signal) => listProjects({ stage, projectType }, signal), `${stage}|${projectType}`);
  const [more, setMore] = useState<Project[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [moreError, setMoreError] = useState<Problem | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setMore([]);
    setMoreError(null);
    setCursor(state.data?.nextCursor);
  }, [state.data]);

  async function loadMore() {
    if (!cursor) return;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const page = await listProjects({ stage, projectType, cursor });
      setMore((rows) => [...rows, ...page.items]);
      setCursor(page.nextCursor);
    } catch (error) {
      setMoreError(describeProblem(error));
    } finally {
      setLoadingMore(false);
    }
  }

  const filtered = stage !== "" || projectType !== "";
  const rows = [...(state.data?.items ?? []), ...more];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="All projects"
        description="Every project in your legal entities, ordered by project id. Open a project to move it to another stage."
        actions={
          <Link className={buttonClassName("primary")} to="/projects/new">
            New project
          </Link>
        }
      />
      <div className={styles.toolbar}>
        <FormField id="projects-stage" label="Stage">
          {(control) => (
            <Select {...control} value={stage} onChange={(e) => setStage(e.target.value as Stage | "")}>
              <option value="">Any stage</option>
              {STAGES.map((s) => (
                <option key={s} value={s}>
                  {stageLabel[s]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="projects-type" label="Project type">
          {(control) => (
            <Select {...control} value={projectType} onChange={(e) => setProjectType(e.target.value as ProjectType | "")}>
              <option value="">Any type</option>
              {PROJECT_TYPES.map((t) => (
                <option key={t} value={t}>
                  {typeLabel[t]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </div>
      {state.error ? (
        <Alert tone="danger" title="Could not load projects" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      ) : (
        <DataTable
          label="Projects"
          columns={columns}
          rows={rows}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={
            filtered
              ? { title: "No projects match these filters", description: "Choose another stage or project type." }
              : { title: "No projects yet", description: "Projects you create appear here." }
          }
          onRowOpen={(row) => navigate(`/projects/${encodeURIComponent(row.id)}`)}
        />
      )}
      {moreError ? (
        <Alert tone="danger" title="Could not load more projects" live="alert">
          {moreError.message} ({moreError.code})
        </Alert>
      ) : null}
      {cursor && !state.error ? (
        <div className={styles.more}>
          <Button onClick={loadMore} loading={loadingMore}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

/** The button text for a stage move; leaving ON_HOLD reads as resuming. */
export function moveText(from: Stage, to: Stage): string {
  if (to === "IN_PROCESS") return from === "ON_HOLD" ? "Resume work" : "Start work";
  if (to === "ON_HOLD") return "Put on hold";
  if (to === "FINISHED") return "Finish project";
  return `Move to ${stageLabel[to]}`;
}

/** Project detail (PRD §8.41.6): header fields and stage moves gated by ProjManage on the service. */
export function ProjectDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getProject(id, signal), id);
  const [latest, setLatest] = useState<Project | null>(null);
  const [busy, setBusy] = useState<Stage | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [moved, setMoved] = useState<string | null>(null);
  const project = latest && latest.id === id ? latest : state.data;

  if (state.error && !project) {
    if (state.error.status === 404) {
      return (
        <div className={styles.stack}>
          <PageHeader title="Project" />
          <Card>
            <EmptyState
              icon="grid"
              title="Project not found"
              description={`There is no project ${id} in your legal entities.`}
              action={
                <Link className={buttonClassName("secondary")} to="/projects/list">
                  Back to projects
                </Link>
              }
            />
          </Card>
        </div>
      );
    }
    return (
      <div className={styles.stack}>
        <PageHeader title="Project" />
        <Alert tone="danger" title="Could not load the project" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          {state.error.message} ({state.error.code})
        </Alert>
      </div>
    );
  }
  if (!project) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Project" />
        <span className="visually-hidden">Loading the project…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  const current = project;

  async function move(to: Stage) {
    setBusy(to);
    setFailure(null);
    setMoved(null);
    try {
      const next = await transitionProject(current.id, to, current.version);
      setLatest(next);
      setMoved(`Project ${next.id} is now ${stageLabel[next.stage].toLowerCase()}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  function reload() {
    setLatest(null);
    setFailure(null);
    state.reload();
  }

  const stale = failure?.code === "conflict_version";
  const facts: [string, ReactNode, boolean?][] = [
    ["Project type", typeLabel[current.projectType]],
    ["Project group", current.projectGroupId, true],
    ["Parent project", current.parentProjectId ? <Link to={`/projects/${encodeURIComponent(current.parentProjectId)}`}>{current.parentProjectId}</Link> : dash, true],
    ["Legal entity", current.legalEntityId, true],
    ["Customer", current.customerPartyId ?? dash, true],
    ["Contract", current.contractId ?? dash, true],
    ["Currency", current.currency, true],
    ["Start date", current.startDate, true],
    ["End date", current.endDate ?? dash, true],
    ["Version", current.version],
  ];
  const moves = nextStages[current.stage];

  return (
    <div className={styles.stack}>
      <PageHeader
        title={`Project ${current.id}`}
        meta={<StageBadge stage={current.stage} />}
        actions={
          <Link
            className={buttonClassName("secondary")}
            to={`/projects/new?parent=${encodeURIComponent(current.id)}&legalEntity=${encodeURIComponent(current.legalEntityId)}`}
          >
            New sub-project
          </Link>
        }
      />
      <Alert tone="success" live="status">
        {moved}
      </Alert>
      {failure ? (
        <Alert
          tone="danger"
          title={stale ? "Someone else changed this project" : "Could not change the stage"}
          live="alert"
          action={stale ? <Button size="sm" onClick={reload}>Reload project</Button> : undefined}
        >
          {stale ? "Reload to see the latest version, then try again." : `${failure.message} (${failure.code})`}
        </Alert>
      ) : null}
      <Card title="Header">
        <dl className={styles.details}>
          {facts.map(([term, value, mono]) => (
            <div key={term} className={styles.entry}>
              <dt className={styles.term}>{term}</dt>
              <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{value}</dd>
            </div>
          ))}
        </dl>
      </Card>
      <Card title="Stage">
        {moves.length === 0 ? (
          <p className={styles.note}>This project is finished. Finished projects cannot move to another stage.</p>
        ) : (
          <div className={styles.stack}>
            <p className={styles.note}>
              Projects move from created to in process, can be put on hold and resumed, and finish from in process. Finishing is final.
            </p>
            <div className={styles.actions} role="group" aria-label="Stage actions">
              {moves.map((to) => (
                <Button
                  key={to}
                  variant={to === "FINISHED" ? "danger" : to === "IN_PROCESS" ? "primary" : "secondary"}
                  loading={busy === to}
                  disabled={busy !== null && busy !== to}
                  onClick={() => void move(to)}
                >
                  {moveText(current.stage, to)}
                </Button>
              ))}
            </div>
          </div>
        )}
      </Card>
    </div>
  );
}
