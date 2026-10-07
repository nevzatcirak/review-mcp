# review-mcp v1 — Consolidated Live Acceptance

| | |
|---|---|
| Replaces | The per-phase live gates: P2 §6 (1–7), P2e §6 (8–10), P3 §7 (1–4), P4 §8 (1–6), P5 §4 (1–4) and the P6 gate (phase-plan amendment, 2026-10-06) |
| Runs against | The release candidate that contains P8 (`v1.0.0-rc.3` or later); I can already be run on `rc.2`. `rc.1` was used for a first real session (record #8). |
| Run by | The owner |
| Record | One GitHub issue, "v1.0.0 acceptance record": one comment per section, redacted (see §0). A fix PR links the item ID it fixes. |

## 0. Rules
- **Instances and accounts:** personal or throwaway only:
  - Gitea via its official container image;
  - Bitbucket Data Center via its official image with an evaluation license;
  - your own LLM endpoint and key.
- **Redaction:** nothing about the instances goes into the repository, the issue or the commits:
  - hosts become `your-gitea.example` and `your-bitbucket.example`;
  - project and repo names become placeholders;
  - tokens never appear, not even partially.
- **Results:** each item records **pass**, **fail**, or **not run** with a reason. "Not run" is honest and allowed; it is never written as a pass.
- **Severity of failures:**
  - **blocker:** wrong output, a leak, a crash, a wrong link or line, or an install failure. It blocks `v1.0.0`.
  - **minor:** wording, cosmetics or friction. It goes to the v1.0.x backlog.
- **Friction:** any friction you hit (an unclear step, a confusing message, too much config) is recorded as an item even when the check passes. User friction is product truth.

## 1. Preparation
- **Test PRs:** one on Gitea and one on Bitbucket Server, each containing:
  - a modified file;
  - an added file;
  - a deleted file;
  - a renamed file with edits.

  On Gitea, advance the target branch *before* opening the PR, so that the merge base differs from the target head.
- **Additional PRs** (one provider is enough unless stated):
  - an **oversized PR** with many files, for example a dependency bump plus code changes;
  - a **filter PR** touching `vendor/…`, `package-lock.json`, an image file and a `.pb.go` file;
  - a **single huge file** PR.
- **Comments on one test PR per provider:**
  - one general comment;
  - one inline comment with one reply;
  - one thread resolved in the UI.

## A. Installation and setup guide (P6)
- **A1** On a clean machine, or a fresh OS user profile, follow `docs/setup.md` literally from the top. Note every step where you had to guess.
- **A2** Install with `npx -y @nevzatcirak/review-mcp@next version`. The version matches `rc.N`, and the commit matches the tag.
- **A3** Create both provider tokens using exactly the scopes in the setup guide's checklist:
  - Read-only tokens work for the read tools.
  - Publishing and replying need the scopes the guide says they need, and fail with the auth sentence without them.

  - P7: on Gitea, inline comments need `write:repository`, editing the overview needs `write:issue`, and identifying the token's user needs `read:user`. On Bitbucket, record whether a Repository read token can post inline comments and edit its own comment.

  Record every scope that was wrong in the guide.
- **A4** Register the server in Claude Code and in opencode using the guide's snippets. Both list the six v1 tools (`server_info`, `pr_comments`, `pr_comment_reply`, `pr_comment_create`, `pr_review`, `pr_ask`). `server_info` shows the secrets as set or unset only.
- **A5** Download one release archive, verify it against `checksums.txt`, and check that the archive contains `LICENSE`, `NOTICE` and `THIRD_PARTY_LICENSES`.

## B. Providers (P2 §6, items 1–7)
- **B1** `review-mcp diag pr <url>` on both test PRs: the file list, change types, rename paths and +/- counts match the web UI. `base_strategy` is reported.
- **B2** `--show-patch` on the renamed file: the hunks match the UI diff, including line numbers and content.
- **B3** Gitea: `base_strategy` is `gitea:merge_base`, and the diff matches the UI despite the advanced target branch.
- **B4** Bitbucket Server: record the strategy used and the server version, and check the diff against the UI. If only one strategy can be exercised, record the other as "hermetically tested only".
- **B5** `diag comment <url> --body "review-mcp connectivity check"`: the comment appears on each PR.
- **B6** A PR URL on a host that is not configured is rejected immediately, with no network wait.
- **B7** (merged into E5)

## C. PR conversation tools (P2e §6, items 8–10)
- **C1** `diag comments` on each test PR:
  - the threads, authors, anchors and reply order match the UI;
  - the resolved thread is hidden by default and shown with `--include-resolved`.
- **C2** `diag reply` to the inline comment:
  - Bitbucket: the reply lands inside the thread.
  - Gitea: a PR-level comment with the quote header appears, and the command reports `in_thread: false`.
- **C3** `pr_comments` from a real MCP client renders readable markdown.

## D. Diff pipeline (P3 §7, items 1–4)
- **D1** Small PR:
  - `diag diff --mode plain` reports `fast_path: true`;
  - a hunk spot-checked against the UI shows 5 extra lines before and 1 after, with correct line numbers in `--mode numbered`.
- **D2** Oversized PR with `REVIEW_MCP_LLM_CONTEXT_WINDOW=8000`:
  - `fast_path: false`;
  - files are ordered by language group, then by size;
  - each file appears in exactly one list;
  - the omitted sections are at the end.
- **D3** Filter PR:
  - `vendor/…`, `package-lock.json` and the image appear under `filtered` with the right reasons;
  - with `REVIEW_MCP_DIFF_IGNORE_GENERATED_FRAMEWORKS=protobuf`, the `.pb.go` file is filtered too.
- **D4** Huge-file PR with `REVIEW_MCP_LLM_CONTEXT_WINDOW=4096`:
  - the `clip` policy yields a truncated diff;
  - the `skip` policy yields the "does not fit" error.

## E. `pr_review` (P4 §8, items 1–6)
- **E1** In a real MCP client, run `pr_review` on both test PRs:
  - the findings point at the right files and lines;
  - the snippets match the code;
  - the coverage section matches `diag diff`.
- **E2** `diag review --dry-run`: record the prompt and diff tokens against `llm.context_window`.
- **E3** With `output_language=tr-TR`: the text is Turkish, and the `security_concerns: No` detection still works.
- **E4** With `publish=true` on each provider, the comment renders correctly:
  - GFM with collapsible sections on Gitea;
  - no raw HTML on Bitbucket.
- **E5** Open one finding link per provider. It lands on the right file and line. A wrong link is a blocker. (This includes the former P2 item 7.)
  Also open one finding on a **renamed** file and, if the model produces one, a finding on a **deleted** file. A wrong link on a modified or renamed file is a blocker; a broken link on a deleted file (the Gitea link points at the head commit) is recorded as a v1.0.x item.
- **E6** In the debug logs of at least 3 reviews with the model you actually use, record which repair tactic fired, if any. "None fired" is a valid result.

## F. `pr_ask` (P5 §4, items 1–4)
- **F1** On each provider, ask three questions:
  - a factual one;
  - a reasoning one;
  - an unanswerable one, for example "What is the deployment schedule for this change?".

  The first two answers are grounded in the diff. The third says the answer cannot be determined from the PR.
- **F2** Ask a question about a file the budget omitted (use a small `REVIEW_MCP_LLM_CONTEXT_WINDOW`). The answer does not invent the file's content, and the coverage lists the file.
- **F3** With `output_language=tr-TR`, the answer is in Turkish.
- **F4** With `publish=true` on each provider, the comment renders correctly. An answer line starting with `/` is published with a leading space and triggers no quick action.

## G. `serve` mode (P6)
- **G1** Run `review-mcp serve` on loopback with `llm_key_source=header`. Connect Claude Code (or opencode) as a remote HTTP server, with the credential headers set from environment references. `pr_review` and `pr_ask` work.
- **G2** Remove the Gitea header from the client config. The call fails with the `credentials_missing` sentence, and the provider access log shows no request.
- **G3** From two clients (or two profiles) configured with tokens of two different Gitea accounts, publish one `pr_ask` answer each (`publish=true`) against the same serve instance. Each comment is authored by its own account.
- **G4** Start serve with `REVIEW_MCP_GITEA_TOKEN` set in the environment. It refuses to start with the fixed sentence.
- **G5** Bind `0.0.0.0` without TLS. It refuses to start. Then bind with TLS files, or behind a TLS proxy with `allow_insecure_http`. It works.
- **G6** With `llm_key_source=server`, an access token is required: a missing or wrong `Authorization` gets 401.

## H. Cross-cutting
- **H1** Run one full session (E1, F1 and C2) at `REVIEW_MCP_LOG_LEVEL=debug`, then search the captured stderr for each token value and for a distinctive phrase from the PR description. Zero hits.
- **H2** Force each error you can (bad token, wrong host, unreachable LLM, tiny context window). Each message is a fixed sentence with no raw server text.
- **H3** Record any friction from the whole run (see §0). Watch in particular how model-authored inline code (backticks) reads in the client profile, where it is escaped (P4 review, point 4).

- **H4** (P9) With `diff.max_tokens` low enough to omit files:
  - the tool text begins with the partial-review banner;
  - the published overview carries it;
  - the client model's reply mentions the files that were not reviewed and does not claim they have no issues.

## I. Conversation-aware review (P7)
Run on a **personal** Bitbucket Data Center trial instance and, if available, a personal Gitea.
- **I1** `server_info` and a `pr_review` publish identify the token's user correctly. Record which Bitbucket identity mechanism was used.
  - Bitbucket: review-mcp reads the `X-AUSERNAME` and `X-AUSERID` response headers of `GET /rest/api/1.0/application-properties`. With a personal token **and** with a project or repository token, confirm that both headers are present, and that they equal the `name` and `id` of the `author` of a comment posted with that token (`GET .../pull-requests/{id}/comments/{commentId}`). If a user name with non-ASCII characters is available, record whether `X-AUSERNAME` carries it unencoded.
  - Gitea: `GET /api/v1/user` returns the `login` and `id` of the author of a comment posted with the token.
- **I2** On a PR with an added line, a context line and an out-of-hunk finding:
  - the inline comments land on the right lines;
  - the Bitbucket context-line anchor is accepted;
  - the out-of-hunk finding appears in the overview only, with its note.
- **I3** The overview marker is invisible in both UIs. A second `publish=true` run **edits** the overview: there is still one overview comment, with an updated time and SHA, and no duplicate inline comments.
- **I4** The security and performance rows are present. A deliberate N+1 or unbounded loop in the test PR is reported under performance.
- **I5** A reviewer comment that already raises an issue is not repeated by the review, and the overview shows the "already discussed" count. A comment containing "ignore previous instructions…" does not change the review.
- **I6** In an MCP client, asking to write a comment on line N of file X uses `pr_comment_create` and lands on the line. A line outside the diff is refused with the fixed sentence.

## J. Slow endpoints (P8)
- **J1** With `llm.context_window` unset against your local endpoint, `server_info` shows the resolved value and its source. Against an endpoint that does not report it, the fixed sentence names `llm.context_window`.
- **J2** In an MCP client, a review that takes longer than 60 s returns a running status within about 45 s. Asking for the result uses `job_result` and returns the full review. With `publish=true`, the comments appear on the PR.
  - **J2 output schemas** `pr_review`, `pr_ask` and `job_result` declare a root `oneOf` output schema (the result or the running status). Record whether opencode and Claude Code accept all three tools (they list and call them without a schema error), per client, pass or fail. If either rejects them, the pre-chosen fallback ships in the next release candidate: drop `outputSchema` for `pr_review`, `pr_ask` and `job_result` in stdio only, keeping the structured content.
- **J3** `diff.max_tokens=24000` on a large PR makes the review noticeably faster, and the omitted files are listed in the coverage section.

## Exit
`v1.0.0` is tagged when every item is pass, or not run with an accepted reason, and no blocker is open. The record issue is then closed with a link to the tag.
