import { useEffect, useMemo, useRef, useState } from 'react'

type Submission = {
  name: string
  email: string
  message: string
}

type SubmissionFromApi = Submission & { timestamp: string }

const API_BASE = ((import.meta.env.VITE_API_BASE as string | undefined) ?? window.location.origin).replace(/\/$/, '')

function App() {
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [message, setMessage] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [connected, setConnected] = useState(false)
  const [leads, setLeads] = useState<Array<Submission & { receivedAt: number }>>([])

  const controllerRef = useRef<AbortController | null>(null)

  const canSubmit = useMemo(() => !!(name.trim() && email.trim() && message.trim()), [name, email, message])

  useEffect(() => {
    // Preload recent leads on mount
    ;(async () => {
      try {
        const res = await fetch(`${API_BASE}/api/leads?limit=20`)
        if (!res.ok) return
        const items: unknown = await res.json()
        if (Array.isArray(items)) {
          const normalized = items
            .map((it) => {
              const rec = it as Partial<SubmissionFromApi>
              if (!rec || !rec.name || !rec.email || !rec.message) return null
              const ts = typeof rec.timestamp === 'string' ? Date.parse(rec.timestamp) : Date.now()
              return { name: rec.name, email: rec.email, message: rec.message, receivedAt: isNaN(ts) ? Date.now() : ts }
            })
            .filter(Boolean) as Array<Submission & { receivedAt: number }>
          setLeads(normalized)
        }
      } catch {
        // ignore preload errors in MVP
      }
    })()

    const es = new EventSource(`${API_BASE}/api/stream/submissions`)
    es.onopen = () => setConnected(true)
    es.onerror = () => setConnected(false)
    es.addEventListener('submission', (ev: MessageEvent) => {
      try {
        const data: Submission = JSON.parse(ev.data)
        setLeads((prev) => [{ ...data, receivedAt: Date.now() }, ...prev].slice(0, 50))
      } catch {
        // ignore parse errors
      }
    })
    return () => es.close()
  }, [])

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!canSubmit || sending) return
    setSending(true)
    setError(null)
    controllerRef.current?.abort()
    const ctrl = new AbortController()
    controllerRef.current = ctrl
    try {
      const res = await fetch(`${API_BASE}/api/submit-form`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, email, message }),
        signal: ctrl.signal,
      })
      if (!res.ok) {
        const j: unknown = await res.json().catch(() => ({}))
        const msg = (() => {
          if (j && typeof j === 'object' && 'error' in (j as Record<string, unknown>)) {
            const errVal = (j as { error?: unknown }).error
            if (typeof errVal === 'string') return errVal
          }
          return `Request failed: ${res.status}`
        })()
        throw new Error(msg)
      }
      setName('')
      setEmail('')
      setMessage('')
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Failed to submit')
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="container">
      <header>
        <h1>Instant Notification Prototype</h1>
        <p>{connected ? 'Live updates connected' : 'Connecting to live updates…'}</p>
      </header>
      <main>
        <section>
          <h2>Lead Submission</h2>
          <form onSubmit={onSubmit} style={{ display: 'grid', gap: 8, gridTemplateColumns: '1fr 1fr' }}>
            <input
              placeholder="Name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              style={{ gridColumn: '1 / span 1', padding: 8, borderRadius: 6, border: '1px solid var(--border)', background: 'transparent', color: 'inherit' }}
            />
            <input
              placeholder="Email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              type="email"
              style={{ gridColumn: '2 / span 1', padding: 8, borderRadius: 6, border: '1px solid var(--border)', background: 'transparent', color: 'inherit' }}
            />
            <textarea
              placeholder="Message"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              rows={4}
              style={{ gridColumn: '1 / span 2', padding: 8, borderRadius: 6, border: '1px solid var(--border)', background: 'transparent', color: 'inherit' }}
            />
            <div style={{ gridColumn: '1 / span 2', display: 'flex', gap: 12, alignItems: 'center' }}>
              <button disabled={!canSubmit || sending} style={{ padding: '8px 12px', borderRadius: 6, border: '1px solid var(--border)', background: 'var(--accent)', color: 'white' }}>
                {sending ? 'Submitting…' : 'Submit Lead'}
              </button>
              {error && <span style={{ color: '#ef4444' }}>{error}</span>}
            </div>
          </form>
        </section>
        <section style={{ marginTop: 16 }}>
          <h2>Sales Dashboard</h2>
          {leads.length === 0 ? (
            <p className="muted">No leads yet. Submit the form to test.</p>
          ) : (
            <ul style={{ listStyle: 'none', padding: 0, margin: 0, display: 'grid', gap: 8 }}>
              {leads.map((l, idx) => (
                <li key={l.receivedAt + '-' + idx} style={{ border: '1px solid var(--border)', borderRadius: 8, padding: 12, background: '#0f1117' }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
                    <strong>{l.name}</strong>
                    <span className="muted" style={{ fontSize: 12 }}>{new Date(l.receivedAt).toLocaleTimeString()}</span>
                  </div>
                  <div style={{ fontSize: 14 }}>
                    <span className="muted">{l.email}</span>
                  </div>
                  <p style={{ marginTop: 8 }}>{l.message}</p>
                </li>
              ))}
            </ul>
          )}
        </section>
      </main>
    </div>
  )
}

export default App
