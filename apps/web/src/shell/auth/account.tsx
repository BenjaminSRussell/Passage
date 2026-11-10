import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { SESSION_EXPIRED_EVENT } from "../api/http";
import { copy } from "../copy";
import { Alert, Button } from "../ui";
import type { Auth } from "./auth";
import type { Session } from "./oidc";
import styles from "./account.module.css";

interface Account {
  name: string;
  signOut(): void;
}

const AccountContext = createContext<Account | null>(null);

/** Who is signed in and how to sign out; null in builds without sign-in (dev, demo). */
export function useAccount(): Account | null {
  return useContext(AccountContext);
}

export function AccountProvider({ auth, session, children }: { auth: Auth; session: Session; children: ReactNode }) {
  const [expired, setExpired] = useState(false);
  useEffect(() => {
    const onExpired = () => setExpired(true);
    window.addEventListener(SESSION_EXPIRED_EVENT, onExpired);
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, onExpired);
  }, []);
  const account = useMemo<Account>(() => ({ name: session.name ?? session.subject, signOut: () => auth.logout() }), [auth, session]);
  return (
    <AccountContext.Provider value={account}>
      {expired ? (
        <div className={styles.expired}>
          <Alert tone="warning" title={copy.account.expiredTitle} live="alert" action={<Button size="sm" onClick={() => void auth.login()}>{copy.account.signIn}</Button>}>
            {copy.account.expiredBody}
          </Alert>
        </div>
      ) : null}
      {children}
    </AccountContext.Provider>
  );
}

export function UserMenu() {
  const account = useAccount();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const close = (event: Event) => {
      if (event instanceof KeyboardEvent ? event.key === "Escape" : !root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", close);
    document.addEventListener("mousedown", close);
    return () => {
      document.removeEventListener("keydown", close);
      document.removeEventListener("mousedown", close);
    };
  }, [open]);
  if (account === null) return null;
  return (
    <div ref={root} className={styles.menu}>
      <Button variant="ghost" size="sm" aria-expanded={open} aria-haspopup="true" onClick={() => setOpen((v) => !v)}>
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
