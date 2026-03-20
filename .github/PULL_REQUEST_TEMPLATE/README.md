# Pull Request Templates

This directory contains pull request templates for the K8s Stack Manager project.

## Available Templates

1. **Default Template** - Used automatically when creating a PR
   - General purpose template for most changes

2. **Specialized Templates** - Use these by adding query parameters to your PR URL:
   - **Bug Fix**: `?template=bugfix.md`
   - **Feature**: `?template=feature.md`
   - **Documentation**: `?template=documentation.md`

## How to Use Templates

### Default Template
Simply create a new PR, and the default template will be used automatically.

### Specialized Templates
1. Create your branch and push changes
2. Navigate to the repository on GitHub
3. Click "Compare & pull request"
4. Add the query parameter to the URL before filling out the PR form:
   ```
   https://github.com/your-username/k8s-stack-manager/compare/main...your-branch?template=feature.md
   ```
   
## Example
For a bug fix PR:
```
https://github.com/your-username/k8s-stack-manager/compare/main...fix-login-issue?template=bugfix.md
```

## Tips for Creating Good Pull Requests
- Be specific about the changes made
- Link to related issues
- Include test information
- Add screenshots for UI changes
- Respond to review comments promptly

## Adding New Templates
To add a new template:
1. Create a new markdown file in this directory
2. Follow the format of existing templates
3. Update this README to include the new template
