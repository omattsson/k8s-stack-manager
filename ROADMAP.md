# Roadmap

The core platform works. This page lists the open work. The source is the [GitHub issues](https://github.com/omattsson/k8s-stack-manager/issues). The list was checked against the open issues on 2026-10-12. The issue tracker is the current source of truth.

## Operations

- **Backup and restore** procedures for MySQL ([#139](https://github.com/omattsson/k8s-stack-manager/issues/139), priority high, effort medium)
- **Retention** for deployment logs, audit logs, notifications, and delivery logs ([#517](https://github.com/omattsson/k8s-stack-manager/issues/517))

## Features

- **GitHub as a Git provider** ([#122](https://github.com/omattsson/k8s-stack-manager/issues/122), priority medium, effort small)
- **Team and project grouping** for instances and templates ([#120](https://github.com/omattsson/k8s-stack-manager/issues/120), priority medium, effort medium)
- **GitOps mode**: deploy automatically on a Git branch push ([#119](https://github.com/omattsson/k8s-stack-manager/issues/119), priority medium, effort large)
- **Link an existing stack definition to a template**. The analytics template usage shows 0 for imported definitions ([#516](https://github.com/omattsson/k8s-stack-manager/issues/516))

## Known issues

- A change of the cluster quota overwrites the instance quota overrides until the next deploy ([#512](https://github.com/omattsson/k8s-stack-manager/issues/512))

## Maintenance

- **Frontend build plugin**: replace `@vitejs/plugin-react-swc` with `@vitejs/plugin-react` (Vite 8 warning) ([#520](https://github.com/omattsson/k8s-stack-manager/issues/520))
