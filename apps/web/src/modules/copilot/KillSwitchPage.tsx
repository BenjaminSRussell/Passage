import { useState } from "react";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, ConfirmDialog, PageHeader, Skeleton } from "../../shell/ui";
import { getKillSwitch, setKillSwitch, type KillSwitchCommand, type KillSwitchResult, type KillSwitchStatus } from "./api/agents";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface KillSwitchPageProps {
  load?: (signal: AbortSignal) => Promise<KillSwitchStatus>;
  toggle?: (cmd: KillSwitchCommand) => Promise<KillSwitchResult>;
  /** Defaults to the signed-in user's duties; platform.global.admin may also use the global switch. */
  duties?: readonly string[];
}

interface Pending {
  cmd: KillSwitchCommand;
  title: string;
  description: string;
  confirmLabel: string;
}

function formatTime(iso: string | null): string {
  if (iso === null) return "Never";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function StateBadge({ active }: { active: boolean }) {
  return <Badge tone={active ? "danger" : "success"}>{active ? "Halted" : "Running"}</Badge>;
}

function Entry({ term, children, mono = false }: { term: string; children: string; mono?: boolean }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={mono ? `${styles.value} ${styles.mono}` : styles.value}>{children}</dd>
    </div>
  );
}

/**
 * Agent kill switch (FR-47.12, AC-47.6; 9.47.6 agent administration). Shows
 * whether agent runs for the caller's tenant are halted and lets an
 * agents.admin halt or resume them; a platform.global.admin holder can also use the global
 * switch. Every toggle is confirmed first.
 */
export function KillSwitchPage({ load = getKillSwitch, toggle = (cmd) => setKillSwitch(cmd), duties }: KillSwitchPageProps) {
  const principalDuties = usePrincipalDuties();
  const canGlobal = (duties ?? principalDuties).includes("platform.global.admin");
  const state = useLoad(load, "kill-switch");
  const [pending, setPending] = useState<Pending | null>(null);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  async function confirm(p: Pending) {
    setPending(null);
    setSaving(true);
    setFailure(null);
    setNotice(null);
    try {
      const out = await toggle(p.cmd);
      const where = out.scope === "GLOBAL" ? "for every tenant" : "for your tenant";
      setNotice(out.active ? `Agent runs are halted ${where}. Running agents stop within 10 seconds.` : `Agent runs are resumed ${where}.`);
      state.reload();
    } catch (err) {
      setFailure(err instanceof Error ? err.message : "The kill switch could not be changed.");
    } finally {
      setSaving(false);
    }
  }

  const header = (
    <PageHeader
      title="Agent kill switch"
      description="Halt every agent run for your tenant. Running agents finish their current call and stop within 10 seconds, and no agent starts until you resume."
    />
  );

  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header}
        <div className={styles.loading}>
          <span className={styles.visuallyHidden}>Loading kill switch…</span>
          <Skeleton width="1/2" />
          <Skeleton height="tile" />
        </div>
      </div>
    );
  }

  if (state.error || !state.data) {
    return (
      <div className={styles.stack}>
        {header}
        <Alert tone="danger" live="alert" title="We couldn't load the kill switch" action={<Button onClick={state.reload}>Try again</Button>}>
          {state.error ?? "No status was returned."}
        </Alert>
      </div>
    );
  }

  const s = state.data;
  const tenantCmd: KillSwitchCommand = { scope: "TENANT", tenantId: s.tenantId, active: !s.tenant.active };
  const globalCmd: KillSwitchCommand = { scope: "GLOBAL", active: !s.global.active };

  return (
    <div className={styles.stack}>
      {header}
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" live="alert" title="The kill switch was not changed">
          {failure}
        </Alert>
      ) : null}
      {s.stale ? (
        <Alert tone="warning" title="Runs are halted while the runtime reconnects">
          The agent runtime has not heard from the kill switch stream for more than 10 seconds, so it halts every run until it does.
        </Alert>
      ) : null}
      {s.global.active && !s.tenant.active ? (
        <Alert tone="warning" title="Halted by the global switch">
          A platform operator has halted agent runs for every tenant. Your tenant resumes when the global switch is cleared.
        </Alert>
      ) : null}
      <Card
        title="Your tenant"
        actions={
          <Button
            variant={s.tenant.active ? "primary" : "danger"}
            loading={saving}
            onClick={() =>
              setPending(
                s.tenant.active
                  ? {
                      cmd: tenantCmd,
                      title: "Resume agent runs?",
                      description: "Agents can start again for your tenant. Runs the switch stopped do not restart; start them again if they are still needed.",
                      confirmLabel: "Resume agent runs",
                    }
                  : {
                      cmd: tenantCmd,
                      title: "Halt all agent runs?",
                      description: "Every agent run for your tenant stops within 10 seconds and no new run starts until you resume. Nothing is posted by a halted run.",
                      confirmLabel: "Halt agent runs",
                    },
              )
            }
          >
            {s.tenant.active ? "Resume agent runs" : "Halt agent runs"}
          </Button>
        }
      >
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Agent runs</dt>
            <dd className={styles.value}>
              <StateBadge active={s.halted} />
            </dd>
          </div>
          <Entry term="Tenant switch">{s.tenant.active ? "On" : "Off"}</Entry>
          <Entry term="Last changed">{formatTime(s.tenant.changedAt)}</Entry>
          <Entry term="Changed by" mono={s.tenant.byUser !== null}>
            {s.tenant.byUser ?? "Nobody yet"}
          </Entry>
        </dl>
      </Card>
      <Card
        title="Global switch"
        actions={
          canGlobal ? (
            <Button
              variant={s.global.active ? "primary" : "danger"}
              loading={saving}
              onClick={() =>
                setPending(
                  s.global.active
                    ? {
                        cmd: globalCmd,
                        title: "Resume agent runs for every tenant?",
                        description: "Agents can start again in every tenant whose own switch is off.",
                        confirmLabel: "Resume all tenants",
                      }
                    : {
                        cmd: globalCmd,
                        title: "Halt agent runs for every tenant?",
                        description: "This is the break-glass switch: every agent run in every tenant stops within 10 seconds.",
                        confirmLabel: "Halt all tenants",
                      },
                )
              }
            >
              {s.global.active ? "Resume all tenants" : "Halt all tenants"}
            </Button>
          ) : undefined
        }
      >
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>State</dt>
            <dd className={styles.value}>
              <StateBadge active={s.global.active} />
            </dd>
          </div>
          <Entry term="Last changed">{formatTime(s.global.changedAt)}</Entry>
        </dl>
        {canGlobal ? null : <p className={styles.muted}>Only a platform operator can change the global switch.</p>}
      </Card>
      {pending ? (
        <ConfirmDialog
          title={pending.title}
          description={pending.description}
          confirmLabel={pending.confirmLabel}
          cancelLabel="Cancel"
          onConfirm={() => void confirm(pending)}
          onCancel={() => setPending(null)}
        />
      ) : null}
    </div>
  );
}
