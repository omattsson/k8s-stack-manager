---
applyTo: "**/*.{ts,tsx}"
---

# TypeScript/React Frontend Instructions

## Project Setup
Frontend lives in `frontend/`. Built with Vite + React 19 + TypeScript (strict mode). Uses SWC via `@vitejs/plugin-react-swc` for fast compilation.

## Key Architecture
- **Entry**: `src/main.tsx` → `App.tsx` → `routes.tsx`
- **Routing**: `react-router-dom` v7 with `<Routes>` / `<Route>` in `src/routes.tsx`
- **UI Library**: MUI (Material UI) v7 — use MUI components (`Box`, `Paper`, `Typography`, `Alert`, etc.) instead of raw HTML
- **API Client**: Axios instance in `src/api/client.ts` with response interceptor for error logging
- **API Config**: `src/api/config.ts` — `API_BASE_URL` switches between `http://localhost:8081` (dev) and `/api` (prod)
- **WebSocket**: `reconnecting-websocket` library with context provider in `src/context/WebSocketContext.tsx` and hook in `src/hooks/useWebSocket.ts`

## Component Patterns
### Pages
Each page is a directory under `src/pages/` with an `index.tsx` export:
```
src/pages/Health/index.tsx
src/pages/Home/index.tsx
src/pages/Items/index.tsx
```
Register new pages in `src/routes.tsx`:
```tsx
<Route path="/new-page" element={<NewPage />} />
```
And add navigation in `src/components/Layout/index.tsx`.

### Layout
`src/components/Layout/index.tsx` provides the shared shell (AppBar, nav links, footer). All pages render inside this layout. Add new nav links as MUI `Button` with `component={RouterLink}`.

### API Services
Define service objects in `src/api/client.ts` alongside the axios instance:
```typescript
export const healthService = {
  checkLiveness: async () => {
    const response = await api.get('/health/live');
    return response.data;
  },
};
```
Use `try/catch` with `console.error` for error handling in service methods.

## TypeScript Config
- Target: ES2020, strict mode enabled
- `noUnusedLocals` and `noUnusedParameters` are enforced — remove dead code
- Module resolution: `bundler` (Vite-native)
- JSX: `react-jsx` (automatic runtime, no React import needed for JSX)

## State Management
Currently uses React hooks (`useState`, `useEffect`) for local state. No global state library.

## Development
- Dev server: `npm run dev` (Vite on port 3000 with HMR)
- Build: `npm run build` (runs `tsc` then `vite build`)
- Lint: `npm run lint`
- In Docker: `make dev-frontend` (Vite dev server in container)
- Vite proxies `/api` to backend service in Docker (`vite.config.ts`)

## Conventions
- Functional components only (no class components)
- Use `React.FC<Props>` type for components with children props
- Interfaces for component props and API response types (defined in the component file)
- MUI `sx` prop for styling, no separate CSS files
- Async operations in `useEffect` with cleanup for intervals/subscriptions
