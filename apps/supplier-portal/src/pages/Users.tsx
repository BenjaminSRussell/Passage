import { useState, type FormEvent } from "react";
import { get, post } from "../api";
import { Badge, day, Field, messageOf, Notice, Page, State, useLoad } from "../ui";

interface Invite { id: string; email: string; displayName?: string; status: string; expiresAt: string; createdAt: string }
interface Created { invite: Invite; token: string; acceptUrl: string }

/** Vendor administrators invite colleagues. The link is shown once, when the invitation is made: only its hash is stored. */
export function Users({ isAdmin }: { isAdmin: boolean }) {
  const load = useLoad(() => (isAdmin ? get<{ items: Invite[] }>("/api/v1/invites?limit=100") : Promise.resolve({ items: [] as Invite[] })), [isAdmin]);
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [link, setLink] = useState<{ email: string; url: string }>();

  if (!isAdmin) {
    return <Page title="Users"><p>Only a vendor administrator can invite colleagues. Ask your administrator.</p></Page>;
  }

  async function invite(e: FormEvent) {
    e.preventDefault();
    setError("");
    setLink(undefined);
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim())) return setError("Enter your colleague's email address.");
    try {
      const made = await post<Created>("/api/v1/invites", { email: email.trim(), displayName: name.trim() });
      setLink({ email: made.invite.email, url: made.acceptUrl });
      setEmail("");
      setName("");
      load.reload();
    } catch (err) {
      setError(messageOf(err));
    }
  }

  return (
    <Page title="Users">
      <form onSubmit={invite} noValidate>
        <h2>Invite a colleague</h2>
        <Field label="Email address">{(id) => <input id={id} type="email" value={email} maxLength={254} autoComplete="off" onChange={(e) => setEmail(e.target.value)} />}</Field>
        <Field label="Name (optional)">{(id) => <input id={id} value={name} maxLength={140} autoComplete="off" onChange={(e) => setName(e.target.value)} />}</Field>
        {error ? <Notice kind="error">{error}</Notice> : null}
        <button type="submit">Create invitation</button>
      </form>
      {link ? (
        <section aria-label="New invitation">
          <Notice kind="ok">Invitation for {link.email} created. Send them this link; it works once, for 7 days, and is not shown again.</Notice>
          <Field label="Invitation link">{(id) => <input id={id} readOnly value={link.url} onFocus={(e) => e.target.select()} />}</Field>
        </section>
      ) : null}
      <h2>Invitations</h2>
      <State load={load} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Invitations you made</caption>
            <thead><tr><th scope="col">Email</th><th scope="col">Name</th><th scope="col">Sent</th><th scope="col">Expires</th><th scope="col">Status</th><th scope="col">Actions</th></tr></thead>
            <tbody>
              {d.items.map((i) => (
                <tr key={i.id}>
                  <th scope="row">{i.email}</th><td>{i.displayName ?? ""}</td><td>{day(i.createdAt)}</td><td>{day(i.expiresAt)}</td><td><Badge value={i.status} /></td>
                  <td>{i.status === "PENDING" ? <button type="button" className="secondary" onClick={() => post(`/api/v1/invites/${i.id}/revoke`).then(load.reload, (e: unknown) => setError(messageOf(e)))}>Revoke invitation for {i.email}</button> : null}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}
