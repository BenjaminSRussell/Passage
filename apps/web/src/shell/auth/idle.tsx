import { useCallback, useEffect, useRef, useState } from "react";
import { copy } from "../copy";
import { Alert, Button } from "../ui";

declare const __PASSAGE_IDLE_TIMEOUT_MINUTES__: number | undefined;

/** Idle sign-out in minutes from the build (the cloud profile's idleTimeoutMinutes); 0 means off. */
export function configuredIdleMinutes(): number {
  return typeof __PASSAGE_IDLE_TIMEOUT_MINUTES__ === "number" ? __PASSAGE_IDLE_TIMEOUT_MINUTES__ : 0;
}

const ACTIVITY_EVENTS = ["pointerdown", "keydown", "wheel", "touchstart", "scroll"] as const;
const SHARED_KEY = "passage.lastActivity";
const WARN_SECONDS = 60;
const SHARE_EVERY_MS = 5_000;

export interface IdleOptions {
  minutes: number;
  onTimeout: () => void;
  now?: () => number;
}

/**
 * Signs the user out after `minutes` without activity (HIPAA automatic logoff, §164.312(a)(2)(iii)). Activity is a
 * pointer press, key, wheel, touch or scroll in any open tab of this browser (the last activity is shared through
 * localStorage, so a busy tab keeps the quiet one signed in). One minute before the end it asks, and any activity or
 * the button keeps the session. Returns the seconds left while the warning shows, else null.
 */
export function useIdleTimeout({ minutes, onTimeout, now = Date.now }: IdleOptions): { secondsLeft: number | null; stay: () => void } {
  const last = useRef(now());
  const sharedAt = useRef(0);
  const [secondsLeft, setSecondsLeft] = useState<number | null>(null);
  const fired = useRef(false);
  const timeout = useRef(onTimeout);
  timeout.current = onTimeout;

  const touch = useCallback(() => {
    const t = now();
    last.current = t;
    setSecondsLeft(null);
    if (t - sharedAt.current < SHARE_EVERY_MS) return;
    sharedAt.current = t;
    try {
      window.localStorage.setItem(SHARED_KEY, String(t));
    } catch {
      // Storage can be blocked; this tab still times out on its own activity.
    }
  }, [now]);

  useEffect(() => {
    if (minutes <= 0) return undefined;
    const limit = minutes * 60_000;
    const onActivity = () => {
      if (!fired.current) touch();
    };
    const onStorage = (e: StorageEvent) => {
      if (e.key === SHARED_KEY && e.newValue !== null) last.current = Math.max(last.current, Number(e.newValue) || 0);
    };
    for (const name of ACTIVITY_EVENTS) window.addEventListener(name, onActivity, { passive: true });
    window.addEventListener("storage", onStorage);
    const tick = window.setInterval(() => {
      const left = limit - (now() - last.current);
      if (left <= 0) {
        if (!fired.current) {
          fired.current = true;
          timeout.current();
        }
      } else {
        setSecondsLeft(left <= WARN_SECONDS * 1000 ? Math.ceil(left / 1000) : null);
      }
    }, 1000);
    return () => {
      for (const name of ACTIVITY_EVENTS) window.removeEventListener(name, onActivity);
      window.removeEventListener("storage", onStorage);
      window.clearInterval(tick);
    };
  }, [minutes, now, touch]);

  return { secondsLeft, stay: touch };
}

/** The warning one minute before an idle sign-out. Renders nothing while the user is active or when the timeout is off. */
export function IdleSignOut({ minutes, onTimeout, now }: IdleOptions) {
  const { secondsLeft, stay } = useIdleTimeout({ minutes, onTimeout, ...(now !== undefined ? { now } : {}) });
  if (secondsLeft === null) return null;
  return (
    <Alert tone="warning" title={copy.account.idleTitle} live="alert" action={<Button size="sm" onClick={stay}>{copy.account.idleStay}</Button>}>
      {copy.account.idleBody(secondsLeft)}
    </Alert>
  );
}
