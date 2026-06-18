# LLM Platform — Frontend

React + TypeScript frontend for the Meesho cataloging LLM platform. Provides a chat interface, side-by-side model comparison, task management, prompt versioning, circuit breaker status, and admin views.

## Tech Stack

| | |
|---|---|
| Framework | React 19 + TypeScript |
| Build tool | Vite 8 |
| Styling | Tailwind CSS 4 + `@meesho/merlin-ui-tailwind` |
| Token counting | `js-tiktoken` |

## Setup

### 1. Install dependencies

```bash
npm install
```

### 2. Start the backend

The frontend proxies all API calls to `http://localhost:8000` via Vite's dev server proxy. Make sure the [backend](https://github.com/Meesho/cataloging_llm_platform-backend) is running on that port before starting the frontend.

### 3. Start the dev server

```bash
npm run dev
```

App runs at `http://localhost:5173`.

### 4. Build for production

```bash
npm run build
```

Output goes to `dist/`. For production deployments, configure your reverse proxy or CDN to forward API paths (`/run`, `/sessions`, `/auth`, `/v1`, `/health`, `/pricing`, `/feedback`, `/dashboard`) to the backend service.

### 5. Lint

```bash
npm run lint
```

## Project Structure

```
src/
  api/client.ts           — HTTP client (uses Vite proxy → backend :8000)
  auth/                   — Auth context, RBAC permissions, useAuth hook
  components/             — Reusable UI (ChatArea, ModelColumn, Sidebar, MessageBubble…)
  pages/                  — Route-level pages:
                              DashboardPage, ComparePage, TasksPage,
                              VersionsPage, ModelHealthPage, AdminRunsPage,
                              ClientPortalPage
  hooks/                  — useChat, useSessions
  toast/                  — Toast notification provider
  types/index.ts          — Shared TypeScript types
  utils/                  — Schema + token utilities
```

## Pages

| Page | Route | Description |
|---|---|---|
| Dashboard | `/` | Usage stats and recent runs |
| Compare | `/compare` | Side-by-side multi-model comparison |
| Tasks | `/tasks` | Task list and management |
| Versions | `/tasks/:id/versions` | Prompt versioning and deployment |
| Model Health | `/health` | Circuit breaker status per task+model |
| Admin Runs | `/admin/runs` | Full run history (admin only) |
| Client Portal | `/client` | Simplified client-facing interface |

## Related

- [Backend repo](https://github.com/Meesho/cataloging_llm_platform-backend)
