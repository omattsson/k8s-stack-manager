---
description: "Use when creating or modifying React page components"
applyTo: "frontend/src/pages/**/*.tsx"
---

# React Page Guidelines

- Use functional components with hooks (no class components)
- Import MUI components from `@mui/material` for all UI elements
- Include loading states using `CircularProgress` centered in a `Box`
- Include error states using `Alert` with `severity="error"`
- Follow the page pattern from `pages/Health/index.tsx`:
  - `useState` for local state
  - `useEffect` for data fetching on mount
  - Loading → Error → Content rendering hierarchy
- Create a `__tests__/` directory alongside the page with at least one render test
- Use TypeScript interfaces for all component props and API response types
- Page titles use `Typography variant="h4" component="h1"`
- Wrap content in MUI `Box` and `Paper` components for consistent spacing
