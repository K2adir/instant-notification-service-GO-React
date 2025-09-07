Render free plans sleep on inactivity. First request may take 30–60s.

Visit either:

Backend: https://instant-notification.onrender.com/


Frontend: https://instant-notification.vercel.app/

The frontend includes an auto wake-up and reconnect: 

it pings the backend and retries the SSE connection with backoff when showing “Connecting to live updates…”. If it still doesn’t connect, wait a bit or open the backend URL once.

## Data Flow

**Submit:** Frontend posts to POST /api/submit-form on the Go backend.

**Persist:** Backend writes the lead to SQLite and records serverBroadcastAt.

**Broadcast:** Backend pushes the new lead over SSE on GET /api/stream/submissions.

**Display:** Frontend’s EventSource receives the event instantly and prepends it to the list.

**Latency:** Frontend computes submit→server, server→display, submit→display and persists them via POST /api/submissions/:id/latency so they survive refresh.

**Reload:** Frontend preloads recent history via GET /api/leads?limit=20 (includes stored latency, if any).

**Proof (JSON):** View recent entries at:

**Verify your submittion on the backend:** https://instant-notification.onrender.com/api/submit-form?limit=5
