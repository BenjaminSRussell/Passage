import type { ReactNode } from "react";
import type { Auth } from "./auth";
import { AuthGate } from "./AuthGate";

export function AuthRoot({ auth, requireAuth, children }: { auth: Auth | null; requireAuth: boolean; children: (duties?: readonly string[], identity?: string) => ReactNode }) {
  if (auth !== null) return <AuthGate auth={auth}>{(session) => children(session.duties, session.tenantId && session.subject ? `${session.tenantId}.${session.subject}` : undefined)}</AuthGate>;
  if (!requireAuth) return <>{children()}</>;
  return (
    <main>
      <h1>Sign-in is not available yet</h1>
      <p>Sign-in for production builds is not available yet in this version, so the application cannot be opened. No administrator action can enable it today.</p>
    </main>
  );
}
