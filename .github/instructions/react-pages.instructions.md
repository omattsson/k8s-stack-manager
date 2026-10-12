---
description: "Use when you create or change React page components"
applyTo: "frontend/src/pages/**/*.tsx"
---

# React Page Guidelines

- Use functional components with hooks. Do not use class components.
- Import Material UI (MUI) components from `@mui/material` for all UI elements.
- Show a loading state with `LoadingState` (`src/components/LoadingState`) or a centered `CircularProgress` in a `Box`.
- Show an error state with `Alert severity="error"`.
- Follow the page pattern of `pages/StackInstances/Dashboard.tsx` or `pages/Templates/Gallery.tsx`. Domain pages use named files. Simple pages use `index.tsx`. Page-level dialogs such as `DeployPreviewDialog` and `SetupWizard` are in `src/components/`.
  - Use `useState` for local state.
  - Use `useEffect` for data fetching on mount.
  - Render in this order: loading, error, content.
- Create a `__tests__/` directory next to the page. Add tests for the loading, success, and error states.
- Use TypeScript interfaces for component props and API response types.
- Page titles: `Typography variant="h4" component="h1"`.
- Wrap content in MUI `Box` and `Paper` for consistent spacing.
- Role rules: role `user` cannot create or import a definition and sees chart source fields as read-only. Hide those actions. Use `hasAtLeastRole` and `canModifyInstance` from `utils/roles.ts`. Use `requiredRole` on `ProtectedRoute` for role-gated pages. The backend enforces the rules (403).
- Relative timestamps: use `timeAgo()` from `utils/timeAgo.ts` with a MUI `Tooltip` that shows the full date.
- Keyboard shortcuts: register `keydown` listeners in `useEffect`. Skip them when `activeElement` is `INPUT`, `TEXTAREA`, or `SELECT`.
- Bulk operations: use checkbox multi-select, a toolbar with `ConfirmDialog` before destructive actions, and a results dialog with the status of each item.
- Recently used items: store an array of IDs in `localStorage` with JSON validation and a size limit.
