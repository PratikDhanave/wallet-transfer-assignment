Your `mmdc` CLI is happy. GitHub renders a blank box. Here are the 4 syntax rules GitHub enforces that the official CLI doesn't.

I shipped a README with 20 mermaid diagrams. Every block rendered cleanly under `npx @mermaid-js/mermaid-cli`. Three blocks were blank on github.com. Two more rendered the wrong shape. The fixes were one character each, and the lesson is that "valid mermaid" is not a property of mermaid — it's a property of the renderer you happen to be looking at.

Trap 1: curly braces in sequence-diagram messages. Writing `S->>W: Get({from, to})` looks fine to the CLI. GitHub's parser reads `{...}` as a JSON object literal and silently drops the message. Fix: replace with parens — `S->>W: Get((from, to))` — or rewrite to avoid them entirely. The CLI gives you no warning that this is even ambiguous.

Trap 2: semicolons inside parenthetical text. Mermaid sequence diagrams treat `;` as a message separator regardless of where it appears on the line. So `S->>W: Get(from); Get(to)` parses as two messages on one line, the first incomplete, and the diagram fails. CLI tolerates it; GitHub does not. Fix: split into two lines, or swap the semicolon for a comma in prose where semantics permit. `INSERT idempotency_records (PK lock; 23505 on conflict)` becomes `INSERT idempotency_records (PK lock, 23505 on conflict)`.

Trap 3: cylinder shape on a subgraph label. `subgraph DB [(PostgreSQL)]` looks like the natural way to mark a "database" subgraph with the cylinder shape. The cylinder shape `[(...)]` is valid syntax for **nodes**, not for **subgraph labels**. The flowchart parser rejects it outright. Fix: drop the parens — `subgraph DB[PostgreSQL]` — and keep the cylinder shape on the nodes inside the subgraph, where it belongs.

Trap 4: unquoted node labels containing `{` or `<br/>`. A label like `N[Service<br/>{validate, lock}]` mixes characters mermaid treats as special with characters it doesn't, and the GitHub renderer fails differently than the CLI. The escape hatch is documented: wrap the whole label in double quotes. `N["Service<br/>{validate, lock}"]` renders identically on both, every time. Make it the default for any label that contains anything other than plain text.

The meta-rule under all four traps: lowest-common-denominator wins. Build for the strictest renderer in your distribution path. If your README lives on github.com, the GitHub renderer is your lower bound — and the GitHub renderer is stricter than the CLI on at least these four patterns. There may be more. I'm sure I'll find them.

A workflow that catches all four before the PR ships:

1. Render every block with `npx @mermaid-js/mermaid-cli` locally. This catches the outright syntax errors but not the GitHub-only failures.
2. Push the branch and view the README on github.com (a draft PR works fine). Walk every diagram visually. Blank boxes are the silent failure mode; the parser doesn't tell you what went wrong, you just see nothing.
3. If a block is blank, apply the four-trap checklist before reaching for a more general debugger. In my experience these four cover the great majority of GitHub-only mermaid breakage.

The reason these are surprising is that mermaid is a spec, but every renderer ships its own parser, and the parsers disagree on the edge cases. The CLI is the reference renderer in the marketing copy. It is not the renderer your README is being read through.

Which mermaid trap has cost you the most time?
