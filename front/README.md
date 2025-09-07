# Frontend (React + Vite)

This is the React frontend for the Instant Notification prototype. The boilerplate has been removed to provide a clean starting point.

## Scripts

- `npm run dev`: Start the Vite dev server
- `npm run build`: Type-check and build for production
- `npm run preview`: Preview the production build
- `npm run lint`: Run ESLint

## Next Steps

- Add a simple lead submission form
- Open an SSE/WebSocket connection to receive new leads in real time
- Display incoming leads on the dashboard

## Configuration

- Backend env (`back/.env.example`):
  - `PORT`: HTTP port (default `8080`)
  - `SQLITE_PATH`: SQLite file path (default `./app.db`)
  - `ALLOWED_ORIGINS`: Comma-separated list for CORS (e.g. `http://localhost:5173`)
- Frontend env (`front/.env.example`):
  - `VITE_API_BASE`: Backend base URL for API calls. If omitted, the app uses the current origin.
