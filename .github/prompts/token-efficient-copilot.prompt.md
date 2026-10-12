# Token-Efficient Copilot Prompt Templates

Use these templates to use fewer tokens and keep the output quality high. Replace each `<placeholder>` with a real value.

## Global rules block (put it before any task)

```text
Goal: <one sentence>
Scope: <exact files only>
Constraints:
- Be concise.
- Do not recap.
- Do not explain background.
- Do not offer alternatives.
- Output only what I ask for.
- When you edit code, apply the changes directly or give a unified diff.
- Do not change files outside the scope.
Validation:
- Run only the tests and lint that the changed files need.
- Report the failing command and the first error only.
```

## Backend Go handler task

```text
Task: Implement <feature or fix> in a backend handler.
Scope:
- backend/internal/api/handlers/<file>.go
- backend/internal/api/routes/routes.go
- backend/internal/api/handlers/<file>_test.go
Requirements:
- Follow the existing handler patterns (items.go for Item, stack_definitions.go for domain handlers).
- Map errors with mapError. Do not leak internal errors. A 500 returns "Internal server error".
- Check access (owner, devops, admin) before any side effect.
- Keep the change small.
Output:
- Apply the edits directly.
- Then run: cd backend && go test ./internal/api/handlers -run <TestName> -v
- Return the changed files and the test result in 6 lines or fewer.
```

## Frontend React page task

```text
Task: Implement <feature or fix> in a page or component.
Scope:
- frontend/src/pages/<Page>/<file>.tsx
- frontend/src/api/client.ts
- frontend/src/routes.tsx
Requirements:
- Reuse the existing MUI patterns.
- Keep the state and API logic small.
- Do not refactor unrelated code.
Output:
- Apply the edits directly.
- Run the targeted test only: cd frontend && npx vitest run <test file>
- Return the changed files and a one-line behavior summary.
```

## Focused test task

```text
Task: Add or fix tests for <feature>.
Scope:
- <exact test files>
Requirements:
- Use table-driven tests for Go tests where it fits.
- Keep fixtures small.
- Do not rewrite unrelated tests.
Output:
- Apply the edits directly.
- Run only the affected tests.
- Return pass or fail and the first failing assertion, if any.
```

## Code review (low token)

```text
Review scope: <branch, PR, or files>
Priorities:
1. Correctness regressions
2. Security risks
3. Scalability and performance risks
4. Missing tests
Output format:
- Findings only, ordered by severity.
- Each finding: file, line, impact, and a fix in 1 or 2 lines.
- If there are no findings: write "No blocking findings." Then list residual risks in 3 bullets or fewer.
```

## Small edit template

```text
Make this exact change only:
<change>
Files allowed:
- <file1>
- <file2>
Output:
- Apply the edits directly.
- Show a short diff summary only.
```

## Tips

- Name the exact files.
- Ask for direct edits, not long explanations.
- Ask for one command or test at a time.
- Split a large task into 2 to 4 prompts.
- Write "first error only" when you debug.
