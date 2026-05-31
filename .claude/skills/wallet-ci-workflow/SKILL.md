---
name: wallet-ci-workflow
description: Use this skill when working on this wallet-transfer repo's CI / branch / PR workflow — editing files under .github/workflows/, opening a pull request, adjusting branch protection expectations, adding a new required check, debugging a failing CI run, or composing a commit / PR message. Trigger when the user asks to "open a PR", "fix CI", "add a workflow", "add a check", "rebase the branch", "push to main", "what's the commit style", "merge the PR". Trigger especially when the diff touches .github/, Makefile CI targets, or branch-protection-checklist.md. Do NOT trigger for code-only changes that don't touch CI files or version-control workflow, or for one-off git operations that don't involve a PR.
---

# wallet-ci-workflow

The assignment ([branch-protection-checklist.md](../../../branch-protection-checklist.md))
lists `lint-format-test` and `sonarqube` as the **only** required
status checks for merging into `main`. This skill captures the repo's
end-to-end CI / branch / PR workflow so future work stays consistent
with that contract.

If anything here conflicts with the assignment or AGENTS.md, those win.

---

## What's in CI today

| Workflow file | Job name | Required? | Trigger |
|---|---|---|---|
| [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml) | `lint-format-test` | ✅ required | `push`/`pull_request` to `main` |
| [`.github/workflows/sonarqube.yml`](../../../.github/workflows/sonarqube.yml) | `sonarqube` | ✅ required | `push`/`pull_request` to `main` |
| (none) | CodeQL | ❌ — provided by GitHub default code scanning | repo-wide |
| (none) | Claude review bot | ❌ — explicitly excluded (scope creep) | n/a |

The CodeQL and Claude-review workflows were deliberately deleted —
see commit `7b2f0d8`. **Do not re-add them.** GitHub default code
scanning covers the CodeQL pillar at the repository level (Settings →
Code security).

---

## The `ci` workflow (`lint-format-test` job) in detail

It runs on `ubuntu-latest` with a `postgres:16-alpine` service
container, in this order:

1. Checkout
2. `setup-go` with `go-version: '1.24'` (must match the floor in
   `go.mod`)
3. `go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8`
   — built from source with the runner's Go so the lint version
   matches the toolchain
4. `gofmt -l .` (fail on any unformatted file)
5. `go vet ./...`
6. `golangci-lint run ./...`
7. `golangci-lint run --build-tags=integration ./...`
8. `go test -race -cover ./...`
9. `go test -race -tags=integration -count=1 -timeout=300s -p 1 ./...`
   with `DATABASE_URL=postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable`

The `-p 1` on step 9 is mandatory: every package shares the one
Postgres service container; cross-package parallelism would let one
package `TRUNCATE` mid-test in another. See `wallet-integration-test`.

If you change CI, mirror the change in the local `make` targets
(`make test-int`, `make lint`, etc.) so contributors get the same
behaviour locally and in CI.

---

## Branch and PR conventions

- **Default branch:** `main`. Long-lived branches mirroring `main`:
  `develop`, `staging`, `production`, `security`. Treat the latter
  four as deploy targets — they fast-forward from `main`, not the
  other way around.
- **Working branches:** named by intent and scope:
  - `feat/<short-slug>` — new feature
  - `fix/<short-slug>` — bug fix
  - `chore/<short-slug>` — tooling / non-code changes
  - `docs/<short-slug>` — documentation only
  - `ci/<short-slug>` — workflow / CI changes
  - `solution/<author>` — assignment submission branch (current:
    `solution/pratik-dhanave`)
- **PR target:** always `main`. Never open a PR to `develop` /
  `staging` / `production` / `security`; those are downstream.
- **PR title:** matches the leading commit's subject. Under 70 chars.
  Same prefix convention (`feat()`, `fix()`, `chore()`, `docs()`,
  `ci()`, `test()`).
- **PR body:** use the template that's already in the repo. Always
  include a Test plan checklist with the local commands that were
  run.

---

## Commit conventions

Topical commits with conventional prefixes:

- `feat(<scope>):` — new behaviour
- `fix(<scope>):` — bug fix
- `chore(<scope>):` — tooling / metadata
- `docs(<scope>):` — README / AGENTS / skills / comments
- `ci(<scope>):` — workflows / Makefile CI targets
- `test(<scope>):` — test-only changes
- `refactor(<scope>):` — code restructure without behaviour change

Scope examples in this repo: `db`, `domain`, `repository`, `service`,
`handler`, `metrics`, `tracing`, `agents`, `readme`, `make`.

Subject under 70 characters, body wrapped at ~72. Body explains
**why**, not what (the diff explains the what).

Every commit gets the same footer:

```
Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

Author for assignment commits:

```
PratikDhanave <2181663+PratikDhanave@users.noreply.github.com>
```

Pass commit messages via heredoc so multiline formatting survives:

```sh
git commit -m "$(cat <<'EOF'
ci(make): add foo gate

Why this change matters in one paragraph.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)" --author="PratikDhanave <2181663+PratikDhanave@users.noreply.github.com>"
```

---

## The pre-push gate

Run before every push:

```sh
make security-check
# fmt-check -> go vet (default+integration) -> lint (default+integration) -> govulncheck
```

If you need the same coverage CI runs:

```sh
make db-up                                        # docker-compose Postgres
make test                                         # unit, race, cover
DATABASE_URL=postgres://wallet:wallet@localhost:5432/wallet?sslmode=disable \
  make test-int                                   # integration, race, -p 1
```

---

## Opening a PR

```sh
# 1. Push the branch with upstream tracking.
git push -u origin <branch>

# 2. Open the PR with a heredoc body.
gh pr create \
  --base main \
  --head <branch> \
  --title "<prefix>(<scope>): <subject>" \
  --body "$(cat <<'EOF'
## Summary
- bullet 1
- bullet 2

## Why
1-2 sentences on the motivation.

## Test plan
- [x] `make security-check` clean
- [x] `make test` passes
- [x] `DATABASE_URL=... make test-int` passes (with compose Postgres)
- [ ] CI `lint-format-test` stays green
EOF
)"
```

After the PR is open, watch the checks:

```sh
gh pr checks <number>
```

---

## What to do when CI fails

1. **Reproduce locally first.** Most CI failures map to a local
   `make` target. Don't push fixes blindly.
2. **`lint-format-test` is the single source of truth.** If it fails
   but `make security-check` was clean, suspect a version mismatch
   between local Go and the runner's `go-version: '1.24'`. Install
   `go1.24.<latest>` locally and re-run.
3. **Integration test failures with "not found":** almost always
   cross-package contamination because `-p 1` was dropped. Re-add it.
4. **`golangci-lint` version-skew error:** the binary release of
   golangci-lint v1.64.8 was built with an older Go. The workflow
   installs from source via `go install` to sidestep this. If the
   lint version needs a bump, rebuild from source — don't switch to
   the curl-piped binary release.

---

## Anti-patterns

| Pattern | Why it's wrong | Fix |
|---|---|---|
| `git push origin main` | Never push directly to `main` — protected branch + assignment review depends on PR history | Push a feature branch and open a PR |
| `git commit --amend` after pushing | Rewrites public history; reviewers lose the audit trail | Create a NEW commit; squash on merge if needed |
| `git commit --no-verify` | Bypasses local hooks (gosec, fmt-check) | Fix the underlying issue |
| `git push --force` to main | Loses history; breaks anyone who pulled | Never |
| Adding a new required workflow without updating branch-protection-checklist.md | Drift between the assignment's policy and what CI enforces | Edit both in the same PR; have the user sign off |
| Re-adding the CodeQL workflow | Default code scanning is already enabled; the two conflict at SARIF upload | Leave it deleted; document in SECURITY.md |
| Pushing to `develop` / `staging` / `production` directly | They mirror `main`; out-of-band pushes diverge them | Update `main` via PR; fast-forward the downstream branches |
| Skipping the heredoc on a multiline commit | Newlines collapse into a single line under shell escaping | Always use the heredoc pattern shown above |

---

## Useful one-liners

```sh
# How far ahead of main is this branch?
git log --oneline origin/main..HEAD | wc -l

# Recent CI runs on this branch.
gh run list --branch "$(git branch --show-current)" --limit 5

# View open PR(s) for this branch.
gh pr list --head "$(git branch --show-current)"

# Tail the most recent CI run's logs.
gh run view --log $(gh run list --branch "$(git branch --show-current)" --limit 1 --json databaseId -q '.[0].databaseId')
```
