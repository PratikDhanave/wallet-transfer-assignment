# Authoring the AI Transcript for Your Take-Home Assignment: A Field Guide

Submitting a take-home with "I used Claude Code" is the minimum. Submitting a 31-prompt transcript with timestamps and your own typos preserved is what tells a reviewer how you actually think with the tool.

This post is the field guide. It covers why the disclosure matters, what good disclosure actually looks like, the typo-preservation rule, the gitignore decision, the rendering pipeline I use to ship the artefact, and a transcript template you can copy.

The repo this comes from: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment). It's a Go wallet-transfer service built as a take-home, and the assignment included an explicit AI-usage clause.

## The clause

The assignment's AI section is short and worth quoting in spirit. The reviewer wants three things:

1. **The tool.** Which model, which CLI / IDE integration, which version. Not "I used AI"; "I used Claude Code with Claude Opus 4.x via the CLI."
2. **How you used it generally.** A few sentences on the role the tool played: pair-programmer, code generator, reviewer, rubber duck. This is the high-level description.
3. **A transcript of your session.** Either in the repo or emailed with the submission. If even that isn't possible, the list of prompts.

The third bullet is the one most candidates skip or skimp on. It's also the one that tells the reviewer the most.

The assignment is also explicit about the grading lens: good use of AI to produce a quality solution is a positive signal; blind use to produce a solution that the candidate can't defend in a follow-up conversation is not. The transcript is the evidence that distinguishes the two.

## What good disclosure looks like

The deliverable is three layers:

**Layer 1 — a PR note.** Short. Three to five sentences. Names the tool, the role, and points at the transcript. Lives in the PR description, not in the repo. Example shape:

> AI usage: this PR was developed with Claude Code (Claude Opus 4.x via CLI) in a pair-programming style — I drove the design decisions and wrote prompts; the model produced first-draft code I then reviewed, edited, and tested. The full session transcript (31 prompts, verbatim with typos preserved) is attached to my submission email as `AI_TRANSCRIPT.pdf`. Please ask in the review and I'll happily walk through any specific prompt-to-commit link.

**Layer 2 — a verbatim prompt log.** This is the transcript file itself. One entry per prompt, time-ordered, no editing. Format is up to you; I use a flat markdown list with optional inline notes when context matters. Example shape:

```markdown
## Prompt 14 — 2026-05-26 14:22

> add an integration test that fires 1000 concurrent debits at a single
> wallt and asserts exactly 100 succeed when seeded balance is 100x amount

(Note: this was after the deadlock fix landed. Wanted to verify the lock
order held under sustained contention. The test is in
internal/service/transfer_concurrent_test.go.)
```

The `wallt` typo stays. So does any other slip. More on that below.

**Layer 3 — the artefact for the reviewer.** A rendered PDF of the prompt log, attached to the submission email. Reviewers can mark it up, search it, print it. PDF is the format they're used to receiving for things they need to read.

## The typo-preservation rule

The single most common mistake I see in submitted transcripts is people cleaning them up before sending. "implmnt idempotent transfer servce" gets edited to "implement idempotent transfer service" because the candidate doesn't want to look sloppy.

Don't. The typo is signal, not noise.

Reviewers who have used these tools at any scale recognize an authentic prompt the moment they see one. Authentic prompts are messy — they contain typos, abbreviations, mid-sentence rewrites, half-finished thoughts that the model is expected to disambiguate. A transcript with perfect grammar reads like a back-fitted artefact written *after* the work was done, which is the opposite of what you want the reviewer to think.

The corollary: do not include prompts you didn't actually use. If you tried three phrasings before the model gave you what you wanted, log all three. The friction is the evidence. A linear, polished transcript means you either edited it or you weren't paying attention.

A useful heuristic: if a sentence in your transcript could plausibly appear in a published blog post, you've over-cleaned it.

## The gitignore decision

I keep the transcript out of the repo's git history. Specifically:

```gitignore
# AI session transcript — emailed to reviewer per ASSIGNMENT.md §AI usage,
# not pushed to the repo. Keep the file(s) locally so they can be attached
# to the submission email, but never commit them.
AI_TRANSCRIPT.md
AI_TRANSCRIPT.html
AI_TRANSCRIPT.pdf
```

Three reasons.

**Diff pollution.** A 31-prompt transcript is a few thousand lines of prose. Every commit that touches it produces a diff that looks like a big change but is just text. Reviewers scrolling through your PR don't want to wade through prompt history to find the code.

**Clone size.** The PDF is bigger than the entire Go source tree on most assignments. There is no reason for `git clone` to pull it.

**Reviewer ergonomics.** Reviewers want one thing in the repo (the code) and one thing in the email (the transcript). Splitting it that way means they can read the code first, form an opinion, then look at the transcript to confirm or revise. Mixing them in the repo collapses that workflow.

The assignment text in this repo's case explicitly allows either path — "add it to the repo or email it to us with your submission." Email it. Tell the reviewer it's attached. They'll find it.

## The rendering pipeline

The transcript starts as markdown because markdown is what you type. It ends as PDF because PDF is what reviewers read. The middle step is HTML.

The pipeline I use is two commands. Conceptually:

```sh
# Markdown -> HTML with a stylesheet that prints cleanly
python3 -m markdown AI_TRANSCRIPT.md \
  --extension=fenced_code,tables,toc \
  > AI_TRANSCRIPT.html

# HTML -> PDF with headless Chrome (any Chromium variant works)
"$(command -v google-chrome || command -v chromium || command -v 'Google Chrome')" \
  --headless --disable-gpu --no-pdf-header-footer \
  --print-to-pdf=AI_TRANSCRIPT.pdf \
  "file://$PWD/AI_TRANSCRIPT.html"
```

Two artefacts get produced: the HTML (so I can preview in a browser before printing) and the PDF (the actual deliverable). Both are gitignored. Both regenerate from the markdown on demand.

If you don't have Python handy, `pandoc AI_TRANSCRIPT.md -o AI_TRANSCRIPT.pdf` is a one-liner alternative — though it pulls LaTeX, which is heavier. The point isn't the toolchain; the point is that the markdown is the source of truth and the PDF is a build artefact.

A small CSS detail that matters: set a sane `max-width` (around 720px) and a monospace font for the code blocks. Reviewers reading on a 27" monitor with the PDF zoomed to fit window will otherwise get prompt text that runs the full screen width and is unreadable. A two-line stylesheet fixes it.

## A sample transcript section template

Steal this and adapt:

```markdown
# AI Session Transcript

**Tool:** Claude Code (Claude Opus 4.x via the official CLI)
**Session window:** 2026-05-20 → 2026-05-27 (8 days, ~12 hours active)
**Total prompts logged:** 31

This file is the verbatim, time-ordered list of prompts I sent during
the assignment. Typos are preserved. The model's responses are NOT
included — the diff in the repo is the response.

---

## Prompt 1 — 2026-05-20 09:14

> read the assignment and summarise the must-have behaviours and the
> nice-to-haves so I can decide what to scope cut

(Context: first prompt of the session. Wanted a checklist before
designing the schema.)

---

## Prompt 2 — 2026-05-20 09:22

> sketch a postgres schema for wallets + transfers + idempotency.
> money is int64 cents. transfers have state PENDING/COMPLETED/FAILED.
> idempotency_records is keyed by the client-supplied key.

(This produced the schema in internal/db/migrations/001_*.sql, modulo
a CHECK constraint I added in a later prompt.)

---

## Prompt 14 — 2026-05-26 14:22

> add an integration test that fires 1000 concurrent debits at a single
> wallt and asserts exactly 100 succeed when seeded balance is 100x amount

(Verifying the lock-order fix held under sustained contention.)
```

Three things to notice about that template:

The header is short. Tool, window, count, scope note. Reviewers want this in one glance.

Each entry has three fields: a prompt number (so the reviewer can reference "look at #14"), a timestamp (so the reviewer can see your working rhythm — was this a focused four-hour block or twenty sessions of five minutes each?), and the prompt itself. The optional inline note is for context only; the prompt is the artefact.

The `wallt` typo lives.

## A note on what the transcript proves

The transcript is not proof that you wrote the code. It is proof that you authored the prompts. Those are different claims.

What it actually proves, to a careful reviewer:

- You knew what to ask. Bad prompts produce bad code. Good prompts presuppose a mental model of the problem the model doesn't have.
- You knew when to stop. Reviewers can read where you stopped prompting and started reading the diff. If your prompts go "implement X" → "fix the bug in X" → "no, the bug is in the lock order, not the balance check" → "yes, that's right" — that's a person reviewing the output.
- You knew what was outside the tool's reach. The interesting prompts in the transcript are the ones where you're asking the model to do something it can't do well — like reason about the global lock order — and you're supplying the missing context.

The reviewer is going to ask in the follow-up: "walk me through prompt 14, what was happening." If the transcript is real, you'll answer in two sentences. If it isn't, you'll stall. The artefact is also the rehearsal.

## What to do tomorrow

If you're mid-assignment: start logging now. Open `AI_TRANSCRIPT.md` in your editor, paste every prompt you send into it as you send it, leave the typos, save after each entry. Five minutes a session.

If you're submitting tomorrow: add `AI_TRANSCRIPT.md` and rendered variants to `.gitignore`, write the prompt log from your shell history if your tool stored it, render to PDF, attach to the email, write the three-sentence PR note.

If you're hiring: ask for the transcript. The signal is loud either way.

---

If you found this useful, follow for the next post in this series — on how to structure a take-home repo so the reviewer can find the design rationale in under sixty seconds.

Repo: [github.com/PratikDhanave/wallet-transfer-assignment](https://github.com/PratikDhanave/wallet-transfer-assignment)
