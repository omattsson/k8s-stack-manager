# Pull Request Templates

This directory has the pull request templates for the K8s Stack Manager project.

## Available Templates

1. **Default template** (`.github/pull_request_template.md`). GitHub uses it automatically when you create a pull request.
   - It is a general template for most changes.
   - `default.md` in this directory has a longer version of the same template.

2. **Specialized templates.** To use one, add a query parameter to the pull request URL:
   - **Bug fix**: `?template=bugfix.md`
   - **Feature**: `?template=feature.md`
   - **Documentation**: `?template=documentation.md`
   - **Long default**: `?template=default.md`

## How to Use the Templates

### Default Template

Create a new pull request. GitHub fills in the default template.

### Specialized Templates

1. Create your branch and push your changes.
2. Open the repository on GitHub.
3. Click "Compare & pull request".
4. Add the query parameter to the URL before you fill in the form:

   ```
   https://github.com/your-username/k8s-stack-manager/compare/main...your-branch?template=feature.md
   ```

## Example

For a bug fix pull request:

```
https://github.com/your-username/k8s-stack-manager/compare/main...fix-login-issue?template=bugfix.md
```

## Tips for Good Pull Requests

- Use a title in the Conventional Commits format (for example `fix(auth): reject a disabled user`). A CI check validates it.
- Describe the changes in specific words.
- Link the related issues.
- Describe how you tested the change.
- Add screenshots for UI changes.
- Answer review comments promptly.

## Add a New Template

1. Create a new Markdown file in this directory.
2. Use the format of the existing templates.
3. Add the template to this README.
