# Token-Efficient Copilot Prompt Templates

Use these templates to reduce token usage while keeping output high quality.

## Global Rules Block (prepend to any task)

```text
Goal: <one sentence>
Scope: <exact files only>
Constraints:
- Be concise.
- No recap.
- No background explanation.
- No alternatives.
- Output only what is requested.
- If editing code: provide unified diff or apply changes directly.
- Do not touch files outside scope.
Validation:
- Run only required tests/lint for changed files.
- Report failing command and first error only.
```

## Backend Go Handler Task

```text
Task: Implement <feature/fix> in backend handler.
Scope:
- backend/internal/api/handlers/<file>.go
- backend/internal/api/routes/routes.go
- backend/internal/api/handlers/<file>_test.go
Requirements:
- Follow existing handler patterns.
- Use safe error mapping; do not leak internal errors.
- Keep changes minimal.
Output:
- Apply edits directly.
- Then run: cd backend && go test ./internal/api/handlers -run <TestName> -v
- Return changed files + test result summary in <= 6 lines.
```

## Frontend React Page Task

```text
Task: Implement <feature/fix> in page/component.
Scope:
- frontend/src/pages/<Page>/index.tsx
- frontend/src/api/client.ts
- frontend/src/routes.tsx
Requirements:
- Reuse existing MUI patterns and project style.
- Keep state and API logic minimal.
- No unrelated refactors.
Output:
- Apply edits directly.
- Run targeted test only.
- Return changed files + one-line behavior summary.
```

## Focused Test Task

```text
Task: Add or fix tests for <feature>.
Scope:
- <exact test files>
Requirements:
- Table-driven for Go tests where applicable.
- Keep fixtures compact.
- No broad rewrites.
Output:
- Apply edits directly.
- Run only affected tests.
- Return pass/fail + first failing assertion if any.
```

## Code Review (Low-Token)

```text
Review scope: <branch/PR/files>
Priorities:
1. Correctness regressions
2. Security risks
3. Scalability/performance risks
4. Missing tests
Output format:
- Findings only, ordered by severity.
- Each finding: file, line, impact, fix in 1-2 lines.
- If no findings: "No blocking findings." then residual risks in <= 3 bullets.
```

## Small Edit Template

```text
Make this exact change only:
<change>
Files allowed:
- <file1>
- <file2>
Output:
- Apply edits directly.
- Show concise diff summary only.
```

## Token-Saver Tips

- Always pin exact files.
- Ask for direct edits, not long explanations.
- Ask for one command/test at a time.
- Split large tasks into 2-4 prompts.
- Use "first error only" when debugging.
