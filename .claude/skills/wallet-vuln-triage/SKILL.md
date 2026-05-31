---
name: wallet-vuln-triage
description: Use this skill when triaging a security advisory in this wallet-transfer repo — a Dependabot alert, a CodeQL finding, a govulncheck hit, a CVE id surfaced by review, or an upstream GHSA. Trigger when the user asks to "look at the Dependabot alerts", "triage this CVE", "is this vuln exploitable", "dismiss this alert", "bump pgx for the security fix", "what does govulncheck say". Trigger especially when SECURITY.md is being edited, when go.mod is bumped in response to an advisory, or when a Dependabot PR appears for review. Do NOT trigger for routine `go mod tidy` runs that aren't security-driven (use `wallet-deps`), for new feature code with no security implication, or for non-security CI failures.
---

# wallet-vuln-triage

Read [SECURITY.md](../../../SECURITY.md) and [AGENTS.md §1](../../../AGENTS.md)
first. The rules here are the **triage workflow** that should be followed
before bumping a dep, dismissing an alert, or writing a SECURITY.md
note.

If anything here conflicts with `SECURITY.md` or `AGENTS.md`, those win.

---

## The two-question test

For every open advisory ask, in order:

1. **Is the vulnerable code path reachable from this binary?**
   Use `govulncheck -mode=source ./...` (already wired into
   `make govulncheck`). Source mode walks the call graph from `main`
   and reports only what your code actually calls. If govulncheck
   classifies the CVE as "in modules you require, but your code
   doesn't appear to call these vulnerabilities", it's a false
   positive for *this* codebase.

2. **Even if reachable, is it exploitable from the deployed surface?**
   Check the advisory's preconditions against our deployment:
   - GOOS / GOARCH gates (a BSD-only bug doesn't affect Linux).
   - Operating mode gates (a simple-protocol pgx bug doesn't affect
     us because we use the extended protocol).
   - Network position gates (a Windows-runner-only CI action bug
     doesn't affect a self-hosted Linux runner).
   - User-input gates (an `args:` argument injection doesn't affect a
     workflow that passes no `args:`).

A CVE that fails BOTH questions is a candidate for dismissal with
justification. A CVE that passes either must be patched (bump the
dep) or worked around (defence-in-depth).

---

## Template A — confirm a Go-module CVE is unreachable

```sh
# 1. The call-graph scan. Should classify the alert under
#    "modules you require, but your code doesn't appear to call".
make govulncheck

# 2. Confirm the vulnerable package is NOT imported by our code.
#    Use the package name from the advisory's "Affected Packages".
grep -rn '"github.com/jackc/pgx/v5/pgproto3"' --include="*.go" .
# (zero matches = we don't import the vulnerable package)

# 3. Confirm the vulnerable mode is not enabled.
grep -rn "QueryExecModeSimpleProtocol" --include="*.go" .
# (zero matches = we don't request the vulnerable mode)
```

If all three are clean, write the dismissal note. Template:

> **Not affected**
>
> govulncheck `-mode=source` classifies CVE-XXXX as "in modules you
> require, but your code doesn't appear to call these vulnerabilities".
> The vulnerable function `<pkg>.<func>` is in `<package>`, which this
> codebase does not import (`grep -rn '<package>' --include='*.go'`
> returns zero hits). The exploit further requires `<precondition>`,
> which `<workflow.yml | code path>` does not enable.

---

## Template B — confirm a build-tagged CVE is not compiled in

Some advisories only affect specific `//go:build` configurations
(BSD-family, Windows, GOARCH=386, etc.). When the vulnerable code is
gated by a build tag we never use, the binary doesn't contain it.

```sh
# 1. Find the vulnerable file and its build tag.
ls "$(go env GOMODCACHE)/<module>@<version>/<path>/"
head -5 "$(go env GOMODCACHE)/<module>@<version>/<path>/<file>.go"

# Should show something like: //go:build dragonfly || freebsd || …

# 2. Confirm our deployment target doesn't match the tag.
#    Look at .github/workflows/ci.yml (runs-on:) and the deploy
#    docs in README. If both are Linux, BSD-only bugs don't apply.
```

Dismissal note adds: "Vulnerable code is in `<file>.go` gated by
`//go:build <tags>`. Our deployment is Linux/amd64 (CI runner +
production), so the function never compiles into the binary."

---

## Template C — confirm a GitHub-Action CVE is not exploitable

Action-pinned CVEs (Sonar, codecov, etc.) usually require:
- A specific runner OS (often Windows).
- User-controlled input passed through a specific input field.

```sh
grep "runs-on" .github/workflows/<workflow>.yml
grep -B1 -A5 "uses: <action>" .github/workflows/<workflow>.yml
# Confirm: runner is Linux. No `args:` (or `inputs:`) under `with:`
# that interpolates ${{ github.event.* }} or other attacker-influenced
# context.
```

If both hold, dismiss with the per-precondition justification. If
either is uncertain, bump the action version instead — it's a
one-line YAML change.

---

## Decision matrix

| `govulncheck` reachable? | Deployment matches preconditions? | Action |
|---|---|---|
| ❌ no | n/a | **Dismiss with justification** in Dependabot UI; add a one-line entry to SECURITY.md so the reasoning is durable. |
| ✅ yes | ❌ no | **Dismiss** (e.g., Windows-only bug, Linux deploy) with explicit per-precondition note. Add SECURITY.md entry. |
| ✅ yes | ✅ yes | **Patch the dep** (`go get <mod>@<fixed>` + `go mod tidy` + `make security-check`). |
| ❌ no | ✅ yes | **Patch anyway** unless the dep bump forces a regressive change (Go floor, breaking API). Document the tradeoff if you choose to defer. |

---

## When the patch forces a Go-floor bump

The dep set in this repo was deliberately downgraded so that `go.mod`
declares `go 1.24.0`. Bumping pgx ≥ 5.9.0, OpenTelemetry ≥ v1.42.0, or
testcontainers-go ≥ v0.41.0 would force `go 1.25.0`. Before bumping
across that line:

1. Re-run the two-question test. If both fail, dismissal is still the
   right answer.
2. If the patch is genuinely needed (reachable AND exploitable), the
   Go floor bump is itself a security improvement (Go 1.24 series is
   off security backport — see `wallet-deps`). Bump both at once and
   land them in a single PR.

---

## Anti-patterns

| Pattern | Why it's wrong | Fix |
|---|---|---|
| Dismissing a Dependabot alert with "won't fix" and no reasoning | Future reviewers can't tell whether the original triager understood the risk | Always paste the two-question result into the dismissal comment and into SECURITY.md |
| Bumping a dep purely to clear a Dependabot badge, without checking govulncheck | Burns time + risks regression on a non-issue | Run `make govulncheck` first; only bump if reachable |
| Trusting Dependabot's severity rating without reading the advisory | The "critical" 9.8 pgx CVE in this repo is unreachable; the rating is for the package, not your code | Read the advisory; identify the vulnerable function; grep for it |
| Skipping the "preconditions" half of the test | Reachable ≠ exploitable. A BSD-only bug on a Linux box is not an exploit. | Always check build tags / runtime mode / user-input gates |
| Adding a `replace` directive to silence a CVE | Replace directives don't change which version is *evaluated* by govulncheck — they just swap the code at link time. The CVE banner stays. | Bump the require line, not replace |

---

## When in doubt: bump

If the analysis is uncertain or the preconditions are hard to verify,
bump the dep. The downside of an unnecessary bump (a slightly larger
diff, a CI re-run) is much smaller than the downside of leaving a real
exploit open. The two-question test is a discipline for when the
analysis is clean, not a license to skip patches.

---

## Verification after triage

After dismissing or patching:

```sh
make govulncheck         # should classify any remaining hits as non-reachable
make security-check      # composite gate: fmt + vet + lint + govulncheck
git diff SECURITY.md     # confirm the justification is written down
```

Then re-check the Dependabot dashboard to confirm the dismissed
alerts are gone or the patched ones have closed.
