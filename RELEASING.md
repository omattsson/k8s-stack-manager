# Releasing

An app release has a tag `vX.Y.Z`. The Helm chart has its own version and its own release (`k8s-stack-manager-X.Y.Z`).

## Release notes

The workflow `.github/workflows/release-drafter.yml` (Release Drafter) runs after each merge to `main`. It keeps a draft GitHub release for the next version:

- It lists the merged pull requests since the last `vX.Y.Z` release.
- It groups them by the conventional commit type of the pull request title: Security (scope `security`), Features (`feat`), Fixes (`fix`), Documentation (`docs`), Dependencies (`deps`) and Maintenance (all other titles).
- It sets the version: `feat` or a breaking change (`!`) raises the minor version, everything else the patch version.

Give each pull request a conventional title, for example `fix(ui): ...` or `feat(helm): ...`. The configuration is in `.github/release-drafter.yml`.

The draft is a list of titles. Before you publish, add at the top: the chart version, the stackctl version to use, database migrations, and the upgrade notes (changed defaults, removed routes or settings).

## Steps

1. Open a pull request `chore(release): vX.Y.Z` that sets `version` (chart) and `appVersion` (app) in `helm/k8s-stack-manager/Chart.yaml`, and the backend and frontend image tags in its `artifacthub.io/images` annotation (Artifact Hub scans these images). The draft leaves this pull request out.
2. Merge it. The workflow `helm-release.yml` (chart-releaser) publishes the chart release.
3. Open the draft release `vX.Y.Z` on GitHub. Check the version and the target (the merge commit on `main`). Complete the notes, then publish it.
4. The tag `vX.Y.Z` starts `docker-build.yml`: the backend and frontend images for amd64 and arm64.
