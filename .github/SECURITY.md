# Security Policy

## Supported Versions

The project gives security fixes for the latest minor release line.

| Version | Supported |
| ------- | --------- |
| 0.9.x   | Yes       |
| < 0.9   | No        |

## Reporting a Vulnerability

We take the security of this project seriously. If you find a security vulnerability, follow these steps:

1. **Do not disclose the vulnerability in public.** Do not open a public issue or pull request for it.
2. **Send an email to security@example.com** with the details of the vulnerability.
3. Include this information in your report:
   - The type of vulnerability
   - The full path of the source files that relate to the vulnerability
   - The location of the affected code (line numbers)
   - A proof of concept or exploit code (if you have it)
   - The impact of the vulnerability

## What to Expect

- We acknowledge receipt of your report within 48 hours.
- We send a more detailed response within 7 days.
- We work with you to understand and fix the vulnerability.
- We keep you informed of the progress toward a fix and an announcement.
- We can ask you for more information.

## Security Practices

The project uses these security practices:

- Dependabot opens weekly pull requests for dependency updates.
- Each pull request runs `gosec` (Go) and `npm audit` (frontend).
- CodeQL analysis runs on `main`, on pull requests, and every week.
- A weekly Trivy scan checks the repository for known vulnerabilities.
- Code review includes security considerations.

## Security-Related Configuration

Follow these guidelines when you deploy the application:

1. Use HTTPS in production. Set `SECURE_COOKIES=true` (the Helm chart sets it when the ingress has TLS).
2. Set `JWT_SECRET` to a random value of at least 16 characters. Change `ADMIN_PASSWORD` and the MySQL root password from the placeholder values.
3. Set `CORS_ALLOWED_ORIGINS` to the origin of your web UI. The default `*` allows all origins.
4. Set `SELF_REGISTRATION=false` when only `admin` users must create accounts. The Helm chart and Docker Compose set it to `true` by default.
5. Set `ALLOWED_CHART_REPOS` (Helm: `backend.allowedChartRepos`) to the chart repositories that you trust. Helm installs charts with the identity of the backend. An empty list allows every chart source.
6. Keep `backend.rbac.scope: namespace` (default) and `backend.rbac.admissionPolicy.enabled: true`. Then a stack chart can reach only the stack namespaces, and cannot create cluster-scoped objects. The policy needs Kubernetes 1.30 or later.
7. Give the `devops` and `admin` roles only to users who must create definitions, templates, and chart sources. A user with the `user` role cannot change chart sources.
8. Set `KUBECONFIG_ENCRYPTION_KEY`. The backend then encrypts kubeconfig data, registry passwords, and webhook channel secrets at rest. Use a long random value, and keep it stable. A changed key makes the stored data unreadable.
9. Keep `ENABLE_SWAGGER` off in production, unless you need the API documentation.
10. Use the External Secrets Operator (`externalSecrets.enabled`) or another secret store for the secrets of the chart. Do not put secrets in value overrides or shared values. Every user who can see an instance can see its merged values.
11. Sign hook and action requests (`secret_env` in the hook configuration). See [EXTENDING.md](../EXTENDING.md).
12. Give cluster credentials that you register with a kubeconfig only the rights that the stacks need.
13. Keep all dependencies and images up to date.

## Acknowledgments

We thank everyone who reports a vulnerability in a responsible way. If you report a vulnerability to us, we are glad to acknowledge your contribution.
