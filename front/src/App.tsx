import { useEffect, useMemo, useRef, useState } from "react";

type Submission = {
  name: string;
  email: string;
  message: string;
};

type SubmissionFromApi = Submission & {
  id: number;
  timestamp: string;
  clientSubmitAtMs?: number | null;
  serverBroadcastAtMs?: number | null;
  clientSubmitToServerMs?: number | null;
  clientServerToDisplayMs?: number | null;
  clientSubmitToDisplayMs?: number | null;
};

type StreamSubmission = Submission & {
  id: number;
  clientSubmitAt?: number;
  serverBroadcastAt?: number;
};

const API_BASE = (() => {
  const fromEnv = import.meta.env.VITE_API_BASE as string | undefined;
  if (fromEnv) return fromEnv;
  if (typeof window !== "undefined") {
    // Helpful default for local dev (Vite on 5173, backend on 8080)
    if (window.location.port === "5173") return "http://localhost:8080";
    return window.location.origin;
  }
  return "http://localhost:8080";
})().replace(/\/$/, "");

function App() {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [message, setMessage] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [connStatus, setConnStatus] = useState<string>("Connecting to live updates…");
  const [leads, setLeads] = useState<
    Array<
      Submission & {
        receivedAt: number;
        submitToDisplayMs?: number;
        serverToDisplayMs?: number;
        submitToServerMs?: number;
      }
    >
  >([]);

  const controllerRef = useRef<AbortController | null>(null);

  const canSubmit = useMemo(
    () => !!(name.trim() && email.trim() && message.trim()),
    [name, email, message]
  );

  useEffect(() => {
    // Preload recent leads on mount
    (async () => {
      try {
        const res = await fetch(`${API_BASE}/api/leads?limit=20`);
        if (!res.ok) return;
        const items: unknown = await res.json();
        if (Array.isArray(items)) {
          const normalized = items
            .map((it) => {
              const rec = it as Partial<SubmissionFromApi>;
              if (!rec || !rec.name || !rec.email || !rec.message) return null;
              const ts =
                typeof rec.timestamp === "string"
                  ? Date.parse(rec.timestamp)
                  : Date.now();
              return {
                name: rec.name,
                email: rec.email,
                message: rec.message,
                receivedAt: isNaN(ts) ? Date.now() : ts,
                submitToServerMs:
                  typeof rec.clientSubmitToServerMs === "number"
                    ? rec.clientSubmitToServerMs
                    : undefined,
                serverToDisplayMs:
                  typeof rec.clientServerToDisplayMs === "number"
                    ? rec.clientServerToDisplayMs
                    : undefined,
                submitToDisplayMs:
                  typeof rec.clientSubmitToDisplayMs === "number"
                    ? rec.clientSubmitToDisplayMs
                    : undefined,
              };
            })
            .filter(Boolean) as Array<
              Submission & {
                receivedAt: number;
                submitToDisplayMs?: number;
                serverToDisplayMs?: number;
                submitToServerMs?: number;
              }
            >;
          setLeads(normalized);
        }
      } catch {
        // ignore preload errors in MVP
      }
    })();

    const esRef = { current: null as EventSource | null };
    const retryRef = { current: 0 };
    let cleanup = false;

    const attachHandlers = (es: EventSource) => {
      es.onopen = () => {
        setConnStatus("Live updates connected");
        retryRef.current = 0;
      };
      es.onerror = () => {
        setConnStatus("Waking backend and reconnecting…");
        // Proactively ping the backend root to wake Render, then reconnect with backoff
        const attempt = ++retryRef.current;
        fetch(`${API_BASE}/`, { cache: "no-store" }).catch(() => undefined);
        const delay = Math.min(1000 * Math.pow(2, attempt), 8000);
        setTimeout(() => {
          if (cleanup) return;
          try { esRef.current?.close(); } catch (e) { void e }
          const next = new EventSource(`${API_BASE}/api/stream/submissions`);
          esRef.current = next;
          attachHandlers(next);
        }, delay);
      };
      es.addEventListener("submission", (ev: MessageEvent) => {
        try {
          const now = Date.now();
          const data: StreamSubmission = JSON.parse(ev.data);
          const submitToDisplayMs =
            typeof data.clientSubmitAt === "number"
            ? Math.max(0, now - data.clientSubmitAt)
            : undefined;
        const serverToDisplayMs =
          typeof data.serverBroadcastAt === "number"
            ? Math.max(0, now - data.serverBroadcastAt)
            : undefined;
        const submitToServerMs =
          typeof data.clientSubmitAt === "number" &&
          typeof data.serverBroadcastAt === "number"
            ? Math.max(0, data.serverBroadcastAt - data.clientSubmitAt)
            : undefined;

        // Persist latency numbers back to backend for this submission id
        if (typeof data.id === "number") {
          const payload = {
            submitToServerMs,
            serverToDisplayMs,
            submitToDisplayMs,
          };
          // Fire-and-forget; errors are non-fatal for UI
          fetch(`${API_BASE}/api/submissions/${data.id}/latency`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(payload),
          }).catch(() => {});
        }

        setLeads((prev) =>
          [
            {
              name: data.name,
              email: data.email,
              message: data.message,
              receivedAt: now,
              submitToDisplayMs,
              serverToDisplayMs,
              submitToServerMs,
            },
            ...prev,
          ].slice(0, 50)
        );
        } catch {
          // ignore parse errors
        }
      });
    };

    // Initial connect (with a gentle warm-up ping to reduce cold start latency)
    setConnStatus("Connecting to live updates…");
    fetch(`${API_BASE}/`, { cache: "no-store" }).catch(() => undefined);
    const es0 = new EventSource(`${API_BASE}/api/stream/submissions`);
    esRef.current = es0;
    attachHandlers(es0);

    return () => {
      cleanup = true;
      esRef.current?.close();
    };
  }, []);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canSubmit || sending) return;
    setSending(true);
    setError(null);
    controllerRef.current?.abort();
    const ctrl = new AbortController();
    controllerRef.current = ctrl;
    try {
      const res = await fetch(`${API_BASE}/api/submit-form`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name, email, message, clientSubmitAt: Date.now() }),
        signal: ctrl.signal,
      });
      if (!res.ok) {
        const j: unknown = await res.json().catch(() => ({}));
        const msg = (() => {
          if (
            j &&
            typeof j === "object" &&
            "error" in (j as Record<string, unknown>)
          ) {
            const errVal = (j as { error?: unknown }).error;
            if (typeof errVal === "string") return errVal;
          }
          return `Request failed: ${res.status}`;
        })();
        throw new Error(msg);
      }
      setName("");
      setEmail("");
      setMessage("");
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to submit");
    } finally {
      setSending(false);
    }
  }

  return (
    <div className="container">
      <header>
        <h1>Instant Notification Prototype</h1>
        <h5>
          <a
            style={{ color: "white" }}
            href="https://instant-notification.onrender.com/api/submit-form?limit=5"
            target="_blank"
          >
            Verify your submission on the backend:
          </a>
        </h5>
        <p>{connStatus}</p>
      </header>
      <main>
        <section>
          <h2>Lead Submission</h2>
          <form
            onSubmit={onSubmit}
            style={{ display: "grid", gap: 8, gridTemplateColumns: "1fr 1fr" }}
          >
            <input
              placeholder="Name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              style={{
                gridColumn: "1 / span 1",
                padding: 8,
                borderRadius: 6,
                border: "1px solid var(--border)",
                background: "transparent",
                color: "inherit",
              }}
            />
            <input
              placeholder="Email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              type="email"
              style={{
                gridColumn: "2 / span 1",
                padding: 8,
                borderRadius: 6,
                border: "1px solid var(--border)",
                background: "transparent",
                color: "inherit",
              }}
            />
            <textarea
              placeholder="Message"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              rows={4}
              style={{
                gridColumn: "1 / span 2",
                padding: 8,
                borderRadius: 6,
                border: "1px solid var(--border)",
                background: "transparent",
                color: "inherit",
              }}
            />
            <div
              style={{
                gridColumn: "1 / span 2",
                display: "flex",
                gap: 12,
                alignItems: "center",
              }}
            >
              <button
                disabled={!canSubmit || sending}
                style={{
                  padding: "8px 12px",
                  borderRadius: 6,
                  border: "1px solid var(--border)",
                  background: "var(--accent)",
                  color: "white",
                }}
              >
                {sending ? "Submitting…" : "Submit Lead"}
              </button>
              {error && <span style={{ color: "#ef4444" }}>{error}</span>}
            </div>
          </form>
        </section>
        <section style={{ marginTop: 16 }}>
          <h2>Sales Dashboard</h2>
          {leads.length === 0 ? (
            <p className="muted">No leads yet. Submit the form to test.</p>
          ) : (
            <ul
              style={{
                listStyle: "none",
                padding: 0,
                margin: 0,
                display: "grid",
                gap: 8,
              }}
            >
              {leads.map((l, idx) => (
                <li
                  key={l.receivedAt + "-" + idx}
                  style={{
                    border: "1px solid var(--border)",
                    borderRadius: 8,
                    padding: 12,
                    background: "#0f1117",
                  }}
                >
                  <div
                    style={{
                      display: "flex",
                      justifyContent: "space-between",
                      marginBottom: 6,
                    }}
                  >
                    <strong>{l.name}</strong>
                    <span className="muted" style={{ fontSize: 12 }}>
                      {new Date(l.receivedAt).toLocaleTimeString()}
                    </span>
                  </div>
                  <div style={{ fontSize: 14 }}>
                    <span className="muted">{l.email}</span>
                  </div>
                  <p style={{ marginTop: 8 }}>{l.message}</p>
                  <div className="muted" style={{ marginTop: 8, fontSize: 12 }}>
                    Latency: {l.submitToServerMs !== undefined ? `${l.submitToServerMs}ms submit→server` : "—"}
                    {" "}·{" "}
                    {l.serverToDisplayMs !== undefined ? `${l.serverToDisplayMs}ms server→display` : "—"}
                    {" "}·{" "}
                    {l.submitToDisplayMs !== undefined ? `${l.submitToDisplayMs}ms submit→display` : "—"}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      </main>
    </div>
  );
}

export default App;
