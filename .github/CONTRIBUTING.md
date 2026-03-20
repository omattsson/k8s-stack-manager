# Contributing to K8s Stack Manager

Thank you for considering contributing to the K8s Stack Manager project! This document outlines the guidelines and workflows for contributing to this project.

## Code of Conduct

By participating in this project, you agree to abide by our Code of Conduct. Please read it before participating.

## How Can I Contribute?

### Reporting Bugs

1. Check if the bug has already been reported in the Issues tab.
2. If not, create a new issue using the Bug Report template.
3. Include as much relevant information as possible:
   - Clear steps to reproduce
   - Expected vs. actual behavior
   - Screenshots if applicable
   - Environment details

### Suggesting Features

1. Check if the feature has already been suggested in the Issues tab.
2. If not, create a new issue using the Feature Request template.
3. Describe the feature clearly and why it would be valuable.

### Improving Documentation

1. For minor changes (typos, clarification), you can submit a pull request directly.
2. For larger documentation issues, create an issue using the Documentation Issue template.

### Pull Request Process

1. Fork the repository and create a branch from `main`.
2. Make your changes.
3. Ensure your code follows the project's style guidelines.
4. Add or update tests as necessary.
5. Update documentation if needed.
6. Ensure all tests pass.
7. Create a pull request using the appropriate template.

## Development Setup

### Prerequisites

- Docker and Docker Compose
- Go 1.24 or later (for backend development)
- Node.js 20 or later (for frontend development)

### Setup Steps

```bash
# Clone your fork
git clone https://github.com/your-username/k8s-stack-manager.git
cd k8s-stack-manager

# Set up development environment
make setup

# Run the application locally
make run
```

### Running Tests

```bash
# Run all tests
make test

# Run backend tests only
make test-backend

# Run frontend tests only
make test-frontend
```

## Style Guidelines

### Go Code

- Follow the standard Go code style (run `gofmt` before committing)
- Ensure code passes `golint` and `go vet`
- Follow the project's architecture pattern

### TypeScript/React Code

- Follow the ESLint configuration in the project
- Use TypeScript types appropriately
- Follow the component structure in the project

### Commit Messages

- Use clear, descriptive commit messages
- Begin with a short summary (50 chars or less)
- Reference issue numbers when appropriate

## Branching Strategy

- `main` - stable production code
- `dev` - development branch for next release
- `feature/<name>` - for new features
- `fix/<name>` - for bug fixes
- `docs/<name>` - for documentation changes

## Code Review Process

1. All code changes require a review before merging.
2. Address review comments promptly.
3. Keep pull requests focused on a single issue/feature.

## License

By contributing, you agree that your contributions will be licensed under the project's MIT License.
