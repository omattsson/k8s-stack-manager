---
name: release
description: Prepare an app and Helm chart release by following RELEASING.md. Drafts the chore(release) pull request changes and the release notes. Stops before any push, merge or publish.
argument-hint: "[version, for example 0.19.0]"
disable-model-invocation: true
---

# Release

Follow `RELEASING.md`. Read it first: it is the source of the steps, and it wins over this file if the two differ.

Never push, merge, tag or publish a release without the user's approval for that step. Each of those is outward-facing and hard to undo.

## 1. Find the version

1. `git fetch origin main --tags`. Branch from `origin/main`: `git switch -c chore/release-vX.Y.Z origin/main`.
2. Read the draft release for the next version: `gh release list --limit 5`, then `gh release view <draft tag>` if one is listed. Release Drafter proposes the version: `feat` or a breaking change (`!`) raises the minor version, everything else the patch version.
3. When the user gave a version as the argument, use it. Otherwise propose the drafted version and ask for confirmation (`AskUserQuestion`).

## 2. Prepare the pull request

Edit `helm/k8s-stack-manager/Chart.yaml`:

- `version` (chart) and `appVersion` (app).
- The backend and frontend image tags in the `artifacthub.io/images` annotation.
- The `artifacthub.io/changes` annotation: replace the entries of the last version, one per change, with `kind` (`added`, `changed`, `deprecated`, `removed`, `fixed` or `security`) and `description`. Build them from the merged pull requests since the last release: `git log <last tag>..origin/main --oneline`. Check every entry against its pull request.

Run `make helm-lint` and `make helm-test`. Commit as `chore(release): vX.Y.Z`.

## 3. Draft the release notes

Write the text to add at the top of the GitHub release draft, into `$TMPDIR/release-notes.md`:

- the chart version,
- the stackctl version to use,
- database migrations (check `git diff <last tag>..origin/main -- backend/internal/database`),
- upgrade notes: changed defaults, removed routes or settings.

Do not invent a stackctl version. If you cannot find it in the repository, leave a marked gap and ask the user.

## 4. Hand over

Show the user the Chart.yaml diff and the draft notes. Ask before `git push` and `gh pr create`. After the pull request merges, the remaining steps (check and publish the draft release on GitHub) are the user's: list them from `RELEASING.md` steps 2 to 4 and stop.
