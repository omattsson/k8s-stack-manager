# Contributing to K8s Stack Manager

Thank you for your interest in K8s Stack Manager. This document explains how to contribute.

## Code of Conduct

By participating in this project, you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md). Read it before you participate.

## How Can I Contribute?

### Report a Bug

1. Search the Issues tab. Check if someone already reported the bug.
2. If not, create an issue with the Bug Report template.
3. Include as much relevant information as possible:
   - Clear steps to reproduce the bug
   - The expected behavior and the actual behavior
   - Screenshots, if they help
   - Environment details: application version, browser, and how you run the application (Docker Compose or Helm)

To report a security vulnerability, do not use a public issue. See [SECURITY.md](SECURITY.md).

### Suggest a Feature

1. Search the Issues tab. Check if someone already suggested the feature. The [roadmap](../ROADMAP.md) lists the planned work.
2. If not, create an issue with the Feature Request template.
3. Describe the feature and explain why it is valuable.

### Improve the Documentation

1. For a small change (a typo or a clarification), send a pull request directly.
2. For a larger problem, create an issue with the Documentation Issue template.

Follow these rules when you write documentation:

- Write short sentences. Use one idea in each sentence.
- Use the active voice and the present tense.
- Use the same word for the same thing. The main terms are: template, stack definition, stack instance, chart config, cluster, and the roles `user`, `devops`, and `admin`.
- Expand an abbreviation the first time that you use it in a document.
- Do not use idioms, marketing words, or decorative symbols.
- Use placeholders such as `example.com` in examples. Do not use the names of real organizations, hosts, or people.
- Check each fact against the code. Check commands, environment variable names, default values, and routes.

### Pull Request Process

1. Fork the repository, and create a branch from `main`.
2. Make your changes.
3. Follow the style guidelines below.
4. Add or update tests.
5. Update the documentation when the behavior changes.
6. Run the checks that are described in "Running Tests and Checks".
7. Create a pull request with the pull request template.

The pull request title must use the [Conventional Commits](https://www.conventionalcommits.org/) format, for example `fix(deployer): clear a pending release before the upgrade`. A CI check validates the title. The allowed types are `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`, and `deps`.

## Development Setup

### Prerequisites

- Docker and Docker Compose
- Go 1.26 or later (backend development)
- Node.js 22.12 or later (frontend development). CI and the Docker images use Node.js 26.
- Helm 3 (chart development)

### Setup Steps

```bash
# Clone your fork
git clone https://github.com/your-username/k8s-stack-manager.git
cd k8s-stack-manager

# Install the dependencies of the backend and the frontend
make install

# Create the Docker Compose settings (first time only)
cp .env.example .env

# Run the full stack with Docker Compose (frontend on port 3000, API on port 8081)
make dev

# Or run MySQL in Docker and the backend and frontend locally with hot reload
make dev-local
```

The default admin credentials for local development are `admin` / `admin`. See the [README](../README.md) for more options, for example a headless stack.

### Running Tests and Checks

```bash
# Run all unit tests (backend and frontend)
make test

# Run the backend unit tests only
make test-backend

# Run the frontend unit tests only
make test-frontend

# Run the backend unit and integration tests (starts MySQL)
make test-backend-all

# Run the end-to-end tests (starts MySQL and the backend)
make test-e2e

# Run the backend tests with a coverage report (80% threshold)
cd backend && make test-coverage

# Lint the code (go vet, TypeScript type check, ESLint)
make lint

# Check the Helm chart
make helm-lint
make helm-test
```

The pull request checks run these commands for the parts that you change: `go vet` and `go test -short` for the backend, `npm run lint`, `vitest`, and `npm run build` for the frontend, `helm lint` and `make helm-test` for the chart, and a Markdown lint for documentation. A security scan (`gosec` and `npm audit`) runs for each pull request.

## Style Guidelines

### Go Code

- Follow the standard Go code style. Run `gofmt` on the code that you change. Do not mix large formatting changes into a pull request.
- Make sure that the code passes `go vet` (and `golangci-lint` when you have it: `cd backend && make lint`).
- Follow the architecture of the project. See [ARCHITECTURE.md](../ARCHITECTURE.md) and the files in [.github/instructions](instructions/).
- Return `"Internal server error"` for HTTP 500 responses. Do not send raw error messages to clients.
- Write table-driven tests with testify.

### TypeScript and React Code

- Follow the ESLint configuration of the project (`cd frontend && npm run lint`).
- Use TypeScript types for props, API responses, and state.
- Use MUI components, and follow the component structure of the project. See [frontend/README.md](../frontend/README.md).
- Add a TSDoc comment with `@param`, `@returns`, and `@see` (HTTP method and route) to each method of a service object in `frontend/src/api/client.ts`.

### Commit Messages

- Use clear, descriptive commit messages in the Conventional Commits format.
- Begin with a short summary of 50 characters or less where you can.
- Refer to issue numbers when it helps.

## Branching Strategy

- `main` is the only long-lived branch. All pull requests target `main`.
- Use short-lived branches that are named `<type>/<short-name>`. The type is a Conventional Commits type, for example `feat/`, `fix/`, `docs/`, or `chore/`.

## Code Review Process

1. All code changes need a review before the merge.
2. Answer review comments promptly.
3. Keep each pull request focused on one issue or feature.

## License

When you contribute, you agree that your contributions use the project's [MIT License](../LICENSE).
