import { useCallback, useEffect, useState } from "react";

const sample = {
  lines: [
    { account: "Cash", debit: 100, credit: 0 },
    { account: "Revenue", debit: 0, credit: 100 },
  ],
};

const unbalanced = {
  lines: [
    { account: "Cash", debit: 100, credit: 0 },
    { account: "Revenue", debit: 0, credit: 40 },
  ],
};

export default function App() {
  const [health, setHealth] = useState(null);
  const [balance, setBalance] = useState(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    try {
      const healthRes = await fetch("/api/healthz");
      if (!healthRes.ok) {
        throw new Error(`health ${healthRes.status}`);
      }
      setHealth(await healthRes.json());
      const balanceRes = await fetch("/api/v1/trial-balance");
      if (!balanceRes.ok) {
        throw new Error(`trial balance ${balanceRes.status}`);
      }
      setBalance(await balanceRes.json());
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function post(body) {
    const res = await fetch("/api/v1/entries", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    const payload = await res.json();
    if (!res.ok) {
      setMessage(payload.error || "rejected");
    } else {
      setMessage(`Posted entry ${payload.id}`);
    }
    await refresh();
  }

  return (
    <main>
      <p className="eyebrow">Wave 1</p>
      <h1>Passage</h1>
      <p className="lede">hello-ledger placeholder. Postings must balance.</p>
      <section>
        <h2>Service</h2>
        {error ? <p className="error">{error}</p> : null}
        {health ? (
          <p>
            {health.service}: {health.status}
          </p>
        ) : (
          <p>Waiting for hello-ledger.</p>
        )}
      </section>
      <section>
        <h2>Journal</h2>
        <div className="actions">
          <button type="button" onClick={() => post(sample)}>
            Post sample journal
          </button>
          <button type="button" onClick={() => post(unbalanced)}>
            Post unbalanced journal
          </button>
        </div>
        {message ? <p className="message">{message}</p> : null}
      </section>
      <section>
        <h2>Trial balance</h2>
        {balance ? (
          <>
            <ul>
              {balance.accounts.length === 0 ? <li>No postings yet.</li> : null}
              {balance.accounts.map((row) => (
                <li key={row.account}>
                  {row.account}: {row.balance}
                </li>
              ))}
            </ul>
            <p>Total {balance.total}</p>
          </>
        ) : (
          <p>No balance loaded.</p>
        )}
      </section>
    </main>
  );
}
