Your reviewer is going to ask "how did you use AI". The right answer is a checked-in transcript. The wrong answer is a verbal summary in the interview.

Take-home assignments increasingly include an AI-disclosure clause. The wording varies — "describe your usage", "include a note in your PR", "tell us what you prompted" — but the intent is the same. The reviewer wants to know whether you used the tool as an assistant or as a substitute. A one-line "I used Claude Code" tells them nothing. A 31-prompt transcript tells them exactly how you think.

Three reasons to write the transcript.

Transparency. The reviewer can match prompts to commits to test coverage. If you prompted "implement idempotent transfer", they should be able to point at the idempotency test that proves you reviewed what came back. If you prompted "fix this concurrency bug", they should be able to read the bug report and the fix as a paired story. The transcript turns AI usage from a black box into reviewable artefacts.

Reproducibility. If someone wants to redo your work — or if a future contributor wants to extend it — your prompts are the only record of what questions actually got asked. The code is the answer; the transcript is the question. Both matter.

Judgment. Anyone can prompt an LLM. The interesting question is whether you noticed when the output was wrong. Your transcript should show prompts like "this isn't handling the race correctly, the lock order needs to come before the balance read" — that's not the AI doing the work; that's you using the AI as a fast typist while you do the work. Reviewers can tell the difference.

What to capture: every prompt verbatim. Including typos. Including the prompts that didn't work and got rephrased. Including the prompts where you realized halfway through that you were asking the wrong question. The friction is the evidence.

What NOT to capture: the AI's output. Your reviewer reads the diff, not a wall of generated code. The transcript should be your inputs, time-ordered, with no editing. If you want to annotate, do it inline with a brief note ("this prompt was after the test failed locally") — but the prompts themselves are immutable.

The typo-preservation rule is the one most people get wrong. The instinct is to clean up "implmnt idempotent transfer servce with locking" into "implement idempotent transfer service with locking" before you ship. Don't. The typo is what makes the prompt look like a real prompt and not a back-fitted artefact. Reviewers who have used these tools recognize the genre instantly. A clean transcript reads like fiction; a typo-preserved transcript reads like a session.

Gitignore policy: the transcript file itself does not belong in the repo's git history. It pollutes diffs, it inflates clone size, and reviewers want to see code changes, not prose. The right pattern is to add `AI_TRANSCRIPT.md` (and any rendered HTML/PDF variants) to your `.gitignore`, write the file locally as you go, and email it to the reviewer alongside the PR link. That way the artefact exists, the reviewer has it, and the repo stays clean.

A small detail that helps reviewers: render the transcript to PDF before sending. Reviewers can annotate it, search it, mark it up in their PDF reader of choice. A markdown file goes into a viewer; a PDF goes onto a desk. The conversion is a one-liner with a markdown processor plus headless Chrome — pick your stack.

The reviewer who asks "how did you use AI" is not trying to catch you out. They are trying to calibrate how much of the work is yours, and how much of the judgment behind the design choices came from you versus from the tool. A transcript answers both questions in the artefact itself, so the interview can spend its time on the interesting parts.

How are you handling AI disclosure on take-home assignments?
