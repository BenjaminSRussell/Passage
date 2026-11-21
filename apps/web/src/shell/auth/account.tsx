import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { SESSION_EXPIRED_EVENT } from "../api/http";
import { copy } from "../copy";
import { Alert, Button } from "../ui";
import type { Auth } from "./auth";
import type { Session } from "./oidc";
import styles from "./account.module.css";
import { configuredIdleMinutes, IdleSignOut } from "./idle";

interface Account {
  name: string;
  signOut(): void;
  /** Asks for the credentials again before a signature (step-up); the page returns here signed in afresh. */
  reauthenticate(): void;
}

const AccountContext = createContext<Account | null>(null);

/** Who is signed in and how to sign out; null in builds without sign-in (dev, demo). */
export function useAccount(): Account | null {
  return useContext(AccountContext);
}

export function AccountProvider({ auth, session, children, idleMinutes = configuredIdleMinutes() }: { auth: Auth; session: Session; children: ReactNode; idleMinutes?: number }) {
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    const onExpired = () => setExpired(true);
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
  }, []);
  const account = useMemo<Account>(() => ({ name: session.name ?? session.subject, signOut: () => auth.logout(), reauthenticate: () => void auth.reauthenticate() }), [auth, session]);
  return (
    <AccountContext.Provider value={account}>
      {expired ? (
        <div className={styles.expired}>
          <Alert tone="warning" title={copy.account.expiredTitle} live="alert" action={<Button size="sm" onClick={() => void auth.login()}>{copy.account.signIn}</Button>}>
            {copy.account.expiredBody}
          </Alert>
        </div>
      ) : null}
      <IdleSignOut minutes={idleMinutes} onTimeout={() => auth.logout()} />
      {children}
    </AccountContext.Provider>
  );
}

export function UserMenu() {
  const account = useAccount();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (event: Event) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [open]);
  if (account === null) return null;
  return (
    <div
      ref={root}
      className={styles.menu}
      onKeyDown={(event) => {
        // Escape closes the menu first; stop it here so a surrounding dialog (the context sheet) stays open.
        if (open && event.key === "Escape") {
          event.stopPropagation();
          setOpen(false);
          trigger.current?.focus();
        }
      }}
    >
      <Button ref={trigger} variant="ghost" size="sm" aria-expanded={open} aria-haspopup="true" onClick={() => setOpen((v) => !v)}>
        {copy.account.menuLabel}: {account.name}
      </Button>
      {open ? (
        <div className={styles.panel} role="group" aria-label={copy.account.menuLabel}>
          <p className={styles.who}>
            {copy.account.signedInAs} <strong>{account.name}</strong>
          </p>
          <Button size="sm" onClick={account.signOut}>
            {copy.account.signOut}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
