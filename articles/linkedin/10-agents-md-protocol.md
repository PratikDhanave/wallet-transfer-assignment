We wrote one Markdown file that Claude Code, Cursor, Aider, and Continue all read.

CLAUDE.md is a 1-line stub that points to it.

This is the easiest interop win in agent-assisted development.

---

If you have tried more than one AI coding assistant in the same repository, you have already met the lock-in problem. Each tool wants its own instruction file. Claude Code looks for CLAUDE.md. Cursor for `.cursorrules` or `.cursor/rules/*`. Aider for CONVENTIONS.md. Continue for its own JSON. OpenCode for yet another path.

Worst case, you end up writing the same security rules four times in four files, with four slightly different wordings, and the divergence is the next bug.

A quieter pattern is emerging: a single `AGENTS.md` at the repo root. The major coding assistants either already read it directly or have a one-line adapter that defers to it. CLAUDE.md becomes a stub:

  Project instructions live in AGENTS.md so that any agent
  (Claude Code, Cursor, Aider, Continue, OpenCode, ...) picks them up.
  Claude Code follows that file as if it were this one.

That stub is the whole file. The actual instructions live in `AGENTS.md`. Every tool reads the same source of truth.

What should go in AGENTS.md? The same things that would go in a CONTRIBUTING.md if humans were the only audience — but written so an automated agent can act on them without asking. In our wallet-transfer service, the structure looks like this:

- Project context: language, runtime version, HTTP library, database, schema-migration tool, test layout, architecture diagram.
- Numbered security rules. We have 16 of them (S-1 through S-16). Each one names the rule, says why it exists, and shows a correct + wrong code example.
- Architecture invariants. Strict layering: handler depends on service, service on repository, repository on DB, domain has no dependencies on the other layers.
- Database conventions. Always parameterize. Always lock rows that will be mutated. BIGINT for money, TIMESTAMPTZ for time, defence-in-depth at the schema level.
- Concurrency rules. Lock in id-ascending order. Never block on I/O inside a transaction. Hot-path tests assert that 20 concurrent workers do not drain a wallet.
- Idempotency rules. Same response for replays. 409 for same-key-different-body. Idempotency row and side effect commit in the same transaction.
- Testing rules. Integration tests gated by `//go:build integration`. Real Postgres, no DB mocks. Race detector mandatory.
- Code style. gofmt clean. golangci-lint clean. Tutorial-grade comments explaining intent, not restating code.
- Commit conventions. Imperative subject, ≤ 72 chars. No `--no-verify`. No `--force` to shared branches.
- "When NOT to" anti-pattern table. Twelve common mistakes, why each is banned, and the pointer to the correct pattern.
- Skill catalog. A table of every project-scoped Claude Code skill, when it auto-loads, and what it provides — so any agent that supports skills can use them, and any agent that does not can still read the table and follow the same patterns by hand.
- The "when to ask before acting" list. Dropping a column. Editing CI. Adding a dependency. Disabling a CHECK constraint. Touching the locking strategy. Eight items, all of which would be expensive to undo silently.

What does NOT belong in AGENTS.md is tool-specific UI guidance. Which keybindings to use, which slash commands to invoke, which menu to click — that is per-tool. Keep it out. The file should make sense to a fresh agent that has never seen the repo before, regardless of which agent it is.

A few notes on governance.

Treat AGENTS.md as code. Changes go through PR review, the same as application code. A security rule loosening in AGENTS.md is exactly as serious as a security rule loosening in a `.go` file — arguably more, because it propagates to every future change in the repo.

Version it with the code, not with the tooling. The rules in AGENTS.md describe the system today. When the system changes, the file changes in the same commit. This is the difference between a living convention and a stale wiki page.

Keep the stub pattern for any tool that demands its own file. CLAUDE.md → 3 lines pointing to AGENTS.md. `.cursorrules` if you use Cursor → same. The stubs cost almost nothing and they remove the temptation to duplicate.

A measurable win we noticed after consolidating: PR review comments that quote a specific rule (`per S-9, this endpoint needs an idempotency key`) dropped from "long explanation in prose" to "rule number plus link." The agent and the human both have the same numbered shared vocabulary now. A code review where the security argument is "S-3, see AGENTS.md" closes faster than one where the argument is a paragraph.

The deeper point: agent-assisted development is going to keep getting better, and the surface that the agent reads is going to keep getting more important. A single file that every tool respects is the same engineering choice as "one source of truth in a database." You would not build a payments system with the user's balance stored in four places and hope they stay in sync. The instructions you give an agent are the same kind of thing.

Which tools have you tried? Do their AGENTS.md adapters actually work, or do you still keep per-tool files?
