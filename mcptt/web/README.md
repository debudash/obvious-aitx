# MCPTT Dispatcher Console

React + Vite + Tailwind SPA implementing the spec's dispatcher mockup:
login, live roster (presence, priority, functional alias, affiliations),
call panels with dispatcher controls, and the emergency rail with
acknowledgement and archive.

## Run

```sh
# from mcptt/server — start and seed a server on :8080
MCPTT_JWT_SECRET=$(openssl rand -hex 32) go run ./cmd/server &
MCPTT_JWT_SECRET=$(openssl rand -hex 32) go run ./cmd/seed

# from mcptt/web
npm install
npm run dev            # http://localhost:5173, proxies /api and /ws to :8080
```

Demo logins: `dispatcher_1` / `mcptt-demo-2026` (also `bravo_2`,
`supervisor_1`, … — same password; see `mcptt/server/internal/seed`).

## Checks

```sh
npm run lint          # ESLint (type-checked)
npm run typecheck     # tsc --noEmit
npm test              # Vitest — state layer against frames (no network)
npm run e2e           # Playwright vs a live server (scripts/e2e.sh starts one)
```

## Layout

- `src/protocol/` — TypeScript mirror of the Go wire contract. Field names
  must match the server exactly; change them together.
- `src/state/` — pure reducer (`consoleStore`), selectors, and the session
  hook (REST bootstrap + WSS frames, resync on reconnect).
- `src/api/` — REST client and the capability table that gates controls the
  running server build does not expose yet.
- `src/components/` — panels; `src/ws/` — reconnecting socket.
- `e2e/` — Playwright flows against a live seeded server.
