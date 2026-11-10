import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { usePrincipalDuties } from "../../shell/principal";
import { ApiError, defaultFetch, requestUrl } from "../../shell/api/http";
import {
  fetchFeatures,
  fetchSetup,
  loadDemoCompany,
  saveFeatures,
  saveSetupStep,
  type SetupStep,
  type StepStatus,
  type TenantFeature,
} from "../../shell/tenant/api";
import { useTenantSettings } from "../../shell/tenant/TenantSettings";
import { Alert, Badge, Button, buttonClassName, Card, EmptyState, PageHeader, Skeleton, type BadgeTone } from "../../shell/ui";
import { z } from "zod";
import { checks, hasRows } from "./checks";
import { stepGuides } from "./guides";
import styles from "./pages.module.css";

const SETUP_KEY = ["setup", "state"];
const FEATURES_KEY = ["setup", "features"];

const statusLabel: Record<StepStatus, string> = { todo: "To do", done: "Done", skipped: "Skipped" };
const statusTone: Record<StepStatus, BadgeTone> = { todo: "neutral", done: "success", skipped: "info" };

function Loading() {
  return (
    <div aria-busy="true">
      <span className="visually-hidden">Loading…</span>
      <Skeleton width="1/2" />
    </div>
  );
}

function Failure({ error, onRetry }: { error: unknown; onRetry(): void }) {
  return (
    <Alert tone="danger" live="alert" action={<Button onClick={onRetry}>Try again</Button>}>
      <ProblemMessage problem={error} fallback="This could not be loaded." />
    </Alert>
  );
}

/** The first step still to do, which is where a person resumes. */
export function currentStep(steps: SetupStep[]): SetupStep | undefined {
  return steps.find((s) => s.status === "todo");
}

export function SetupWizardPage() {
  const queryClient = useQueryClient();
  const { refresh } = useTenantSettings();
  const setup = useQuery({ queryKey: SETUP_KEY, queryFn: ({ signal }) => fetchSetup(signal), retry: false });
  const [picked, setPicked] = useState<string>();
  const change = useMutation({
    mutationFn: (v: { key: string; status: StepStatus }) => saveSetupStep(v.key, v.status),
    onSuccess: (next) => {
      queryClient.setQueryData(SETUP_KEY, next);
      setPicked(undefined);
      refresh();
    },
  });

  if (setup.isPending) return <Loading />;
  if (setup.isError) return <Failure error={setup.error} onRetry={() => void setup.refetch()} />;
  const { steps, finished, total, complete } = setup.data;
  const open = steps.find((s) => s.key === picked) ?? currentStep(steps);
  const guide = open ? stepGuides[open.key] : undefined;

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Set up your company"
        description="Work through these steps in order. You can leave and come back; your progress is saved."
        actions={
          <Link to="/setup/checklist" className={buttonClassName()}>
            See the checklist
          </Link>
        }
      />
      <p role="status">{complete ? "Setup is finished." : `${finished} of ${total} steps finished.`}</p>
      <progress className={styles.progress} max={total} value={finished} aria-label="Setup progress" />
      {complete ? (
        <Alert tone="success" title="Setup is finished">
          Every step is done or skipped. You can reopen a step below at any time.
        </Alert>
      ) : null}
      <ol className={styles.steps} aria-label="Setup steps">
        {steps.map((step, index) => (
          <li key={step.key} className={styles.step} aria-current={open?.key === step.key ? "step" : undefined}>
            <button type="button" className={styles.stepButton} onClick={() => setPicked(step.key)}>
              <span className={styles.stepNumber}>{index + 1}</span>
              <span className={styles.stepName}>{step.name}</span>
              <Badge tone={statusTone[step.status]}>{statusLabel[step.status]}</Badge>
            </button>
          </li>
        ))}
      </ol>
      {open ? (
        <Card title={open.name} headingLevel={2}>
          <p>{open.description}</p>
          {guide ? (
            <>
              <ul className={styles.help}>
                {guide.help.map((line) => (
                  <li key={line}>{line}</li>
                ))}
              </ul>
              <p>
                <Link to={guide.to} className={buttonClassName()}>
                  {guide.linkLabel}
                </Link>
              </p>
            </>
          ) : null}
          {change.isError ? (
            <Alert tone="danger" live="alert">
              <ProblemMessage problem={change.error} fallback="The step could not be saved." />
            </Alert>
          ) : null}
          <div className={styles.actions}>
            {open.status === "todo" ? (
              <>
                <Button variant="primary" loading={change.isPending} onClick={() => change.mutate({ key: open.key, status: "done" })}>
                  Mark as done
                </Button>
                <Button loading={change.isPending} onClick={() => change.mutate({ key: open.key, status: "skipped" })}>
                  Skip for now
                </Button>
              </>
            ) : (
              <Button loading={change.isPending} onClick={() => change.mutate({ key: open.key, status: "todo" })}>
                Reopen this step
              </Button>
            )}
          </div>
        </Card>
      ) : null}
    </div>
  );
}

const listBody = z.unknown();

type CheckState = "present" | "missing" | "unknown";

export function ChecklistPage() {
  const duties = usePrincipalDuties();
  const { features } = useTenantSettings();
  const shown = checks.filter((c) => duties.includes(c.duty) && (c.feature === undefined || features?.[c.feature] !== false));
  const results = useQueries({
    queries: shown.map((c) => ({
      queryKey: ["setup", "check", c.id],
      queryFn: () => requestUrl("GET", c.path, listBody, defaultFetch),
      retry: false,
    })),
  });
  const state = (i: number): CheckState => {
    const r = results[i];
    return r.isError ? "unknown" : r.isSuccess ? (hasRows(r.data) ? "present" : "missing") : "unknown";
  };
  const pending = results.some((r) => r.isPending);
  const areas = [...new Set(shown.map((c) => c.area))];
  const missing = shown.filter((_, i) => state(i) === "missing").length;

  return (
    <div className={styles.stack}>
      <PageHeader title="Setup checklist" description="What is set up and what is still missing, by area." actions={<Link to="/setup" className={buttonClassName()}>Back to the setup guide</Link>} />
      {pending ? <Loading /> : null}
      {!pending && shown.length === 0 ? (
        <Card>
          <EmptyState icon="grid" title="Nothing to check" description="Your account can't read the lists this checklist looks at. Ask an administrator for access." />
        </Card>
      ) : null}
      {!pending && shown.length > 0 ? (
        <p role="status">{missing === 0 ? "Everything this checklist looks at is set up." : missing === 1 ? "1 thing is still missing." : `${missing} things are still missing.`}</p>
      ) : null}
      {areas.map((area) => (
        <Card key={area} title={area} headingLevel={2}>
          <ul className={styles.checks} aria-label={area}>
            {shown.map((c, i) =>
              c.area === area ? (
                <li key={c.id} className={styles.check}>
                  <Badge tone={state(i) === "present" ? "success" : state(i) === "missing" ? "warning" : "neutral"}>
                    {state(i) === "present" ? "Ready" : state(i) === "missing" ? "Missing" : "Could not check"}
                  </Badge>
                  <span>{state(i) === "present" ? c.present : state(i) === "missing" ? c.missing : c.present.replace(/ (added|defined|set up|in place)$/, "")}</span>
                  {state(i) !== "present" ? <Link to={c.to}>Fix this</Link> : null}
                </li>
              ) : null,
            )}
          </ul>
        </Card>
      ))}
    </div>
  );
}

function FeatureRow({ feature, disabled, onToggle }: { feature: TenantFeature; disabled: boolean; onToggle(on: boolean): void }) {
  const id = `feature-${feature.key}`;
  return (
    <li className={styles.feature}>
      <input id={id} type="checkbox" checked={feature.enabled} disabled={disabled} onChange={(e) => onToggle(e.target.checked)} />
      <label htmlFor={id}>
        <span className={styles.featureName}>{feature.name}</span>
        <span className={styles.featureDescription}>{feature.description}</span>
      </label>
    </li>
  );
}

export function FeaturesPage() {
  const queryClient = useQueryClient();
  const { refresh } = useTenantSettings();
  const catalog = useQuery({ queryKey: FEATURES_KEY, queryFn: ({ signal }) => fetchFeatures(signal), retry: false });
  const save = useMutation({
    mutationFn: (change: { template: string } | { features: Record<string, boolean> }) => saveFeatures(change),
    onSuccess: (next) => {
      queryClient.setQueryData(FEATURES_KEY, next);
      refresh();
    },
  });

  if (catalog.isPending) return <Loading />;
  if (catalog.isError) return <Failure error={catalog.error} onRetry={() => void catalog.refetch()} />;
  const { features, templates } = catalog.data;
  const dependencyErrors = save.error instanceof ApiError ? Object.values(save.error.fields) : [];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Features"
        description="Turn on what your company uses. What you turn off disappears from menus and forms; people keep their permissions, and you can turn it back on at any time."
        actions={<Link to="/setup" className={buttonClassName()}>Back to the setup guide</Link>}
      />
      {save.isError ? (
        <Alert tone="danger" live="alert">
          {dependencyErrors.length > 0 ? dependencyErrors.join(" ") : <ProblemMessage problem={save.error} fallback="The change could not be saved." />}
        </Alert>
      ) : null}
      <Card title="Start from a template" headingLevel={2}>
        <ul className={styles.templates} aria-label="Industry templates">
          {templates.map((t) => (
            <li key={t.key} className={styles.template}>
              <span className={styles.featureName}>{t.name}</span>
              <span className={styles.featureDescription}>{t.description}</span>
              <Button loading={save.isPending} onClick={() => save.mutate({ template: t.key })} aria-label={`Use the ${t.name} template`}>
                Use this template
              </Button>
            </li>
          ))}
        </ul>
      </Card>
      <Card title="Features" headingLevel={2}>
        <ul className={styles.featureList} aria-label="Features">
          {features.map((f) => (
            <FeatureRow key={f.key} feature={f} disabled={save.isPending} onToggle={(on) => save.mutate({ features: { [f.key]: on } })} />
          ))}
        </ul>
      </Card>
    </div>
  );
}

export function DemoCompanyPage() {
  const load = useMutation({ mutationFn: () => loadDemoCompany() });
  return (
    <div className={styles.stack}>
      <PageHeader title="Demo company" description="Fill this tenant with a sample manufacturer (three legal entities, plants, warehouses and teams), for trials and sales demos." />
      <Alert tone="info">
        Loading the demo company adds sample records. It never deletes or changes what you already have, so it is safe to repeat. Use it on a trial or demo tenant, not on a live company.
      </Alert>
      <Card>
        <div className={styles.actions}>
          <Button variant="primary" loading={load.isPending} onClick={() => load.mutate()}>
            Load the demo company
          </Button>
        </div>
        {load.isSuccess ? (
          <p role="status">
            {load.data.created === 0 ? `The demo company was already loaded: ${load.data.present} records are in place.` : `Added ${load.data.created} sample records. ${load.data.present} were already there.`}
          </p>
        ) : null}
        {load.isError ? (
          <Alert tone="danger" live="alert">
            <ProblemMessage problem={load.error} fallback="The demo company could not be loaded." />
          </Alert>
        ) : null}
      </Card>
    </div>
  );
}
