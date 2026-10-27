import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Alert, Button, Card, FormField, Input, PageHeader } from "../../shell/ui";
import { badgeLogin, createRegistrations, describeProblem, newKey, type KioskSession, type Problem, type RegistrationInput } from "./api/client";
import { formatDateTime } from "./format";
import { KIOSK_DEVICE_ID, KioskJobs } from "./KioskJobs";
import styles from "./pages.module.css";

export { KIOSK_DEVICE_ID };

type Notice = { tone: "success" | "info"; text: string } | null;

/**
 * Shared kiosk: a worker scans a badge (FR-25.1), clocks in or out (FR-25.2),
 * starts and stops jobs and bundles while clocked in (FR-25.3), and is signed
 * out after the idle timeout the service returns.
 */
export function KioskPage() {
  const [badge, setBadge] = useState("");
  const [badgeError, setBadgeError] = useState<string | undefined>();
  const [session, setSession] = useState<KioskSession | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [notice, setNotice] = useState<Notice>(null);
  const [busy, setBusy] = useState(false);
  // A clock action that failed keeps its client UUID, so retrying it can never punch twice.
  const pending = useRef<RegistrationInput | null>(null);

  const signOut = useCallback((text?: string) => {
    setSession(null);
    setBadge("");
    setFailure(null);
    pending.current = null;
    setNotice(text ? { tone: "info", text } : null);
  }, []);

  // Sign out after the idle timeout; any key or pointer input restarts it.
  useEffect(() => {
    if (!session) return undefined;
    const minutes = session.idleTimeoutMinutes;
    const ms = minutes * 60_000;
    const expire = () => signOut(`Signed out after ${minutes} ${minutes === 1 ? "minute" : "minutes"} without input.`);
    let timer = window.setTimeout(expire, ms);
    const restart = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(expire, ms);
    };
    window.addEventListener("pointerdown", restart);
    window.addEventListener("keydown", restart);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("pointerdown", restart);
      window.removeEventListener("keydown", restart);
    };
  }, [session, signOut]);

  async function signIn(event: FormEvent) {
    event.preventDefault();
    const scanned = badge.trim();
    setNotice(null);
    setFailure(null);
    if (scanned.length < 1 || scanned.length > 64) {
      setBadgeError("Scan your badge or type its number.");
      return;
    }
    setBadgeError(undefined);
    setBusy(true);
    try {
      setSession(await badgeLogin(scanned));
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "SF-0001") setBadgeError("This badge is not recognized. Ask your supervisor.");
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  async function clock(kind: "clock-in" | "clock-out") {
    if (!session) return;
    if (pending.current?.kind !== kind) {
      pending.current = { id: newKey(), workerId: session.workerId, kind, startedAt: new Date().toISOString(), deviceId: KIOSK_DEVICE_ID };
    }
    const item = pending.current;
    setBusy(true);
    setFailure(null);
    setNotice(null);
    try {
      await createRegistrations([item]);
      pending.current = null;
      setSession({ ...session, clockedInAt: kind === "clock-in" ? item.startedAt : null });
      setNotice({ tone: "success", text: `${kind === "clock-in" ? "Clocked in" : "Clocked out"} at ${formatDateTime(item.startedAt)}.` });
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Kiosk" description="Scan your badge to clock in or out. The kiosk signs you out after a few minutes without input." />
      {notice ? (
        <Alert tone={notice.tone} live="status">
          {notice.text}
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title={session ? "The punch was not recorded" : "Could not sign in"} live="alert">
          {failure.message} ({failure.code}){session ? " Press the button again to retry the same punch." : ""}
        </Alert>
      ) : null}
      <div className={`${styles.kiosk} ${styles.stack}`}>
        {session ? (
          <Card title={session.name} actions={<Button variant="ghost" onClick={() => signOut()}>Sign out</Button>}>
            <div className={styles.stack}>
              <p className={styles.value}>
                {session.clockedInAt ? `Clocked in since ${formatDateTime(session.clockedInAt)}.` : "You are clocked out."}
              </p>
              {session.clockedInAt ? (
                <Button className={styles.kioskButton} variant="danger" loading={busy} onClick={() => void clock("clock-out")}>
                  Clock out
                </Button>
              ) : (
                <Button className={styles.kioskButton} variant="primary" loading={busy} onClick={() => void clock("clock-in")}>
                  Clock in
                </Button>
              )}
              {session.clockedInAt ? null : <p className={styles.value}>Clock in to start jobs.</p>}
            </div>
          </Card>
        ) : (
          <Card>
            <form className={styles.stack} onSubmit={signIn} noValidate>
              <FormField id="kiosk-badge" label="Badge" hint="Scan the badge, or type its number and press Sign in." error={badgeError}>
                {(control) => (
                  <Input
                    {...control}
                    value={badge}
                    autoComplete="off"
                    maxLength={64}
                    invalid={Boolean(badgeError)}
                    onChange={(event) => setBadge(event.target.value)}
                  />
                )}
              </FormField>
              <Button className={styles.kioskButton} type="submit" variant="primary" loading={busy}>
                Sign in
              </Button>
            </form>
          </Card>
        )}
        {session?.clockedInAt ? (
          <KioskJobs
            key={session.workerId}
            workerId={session.workerId}
            openJobs={session.openJobs ?? []}
            onOpenJobsChange={(openJobs) => setSession((s) => (s ? { ...s, openJobs } : s))}
          />
        ) : null}
      </div>
    </div>
  );
}
