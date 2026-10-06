# WP-PR-2e — PR Conversation Tools: Specification

| | |
|---|---|
| Decision | X-9 in `docs/design/v1-design-decisions.md` (added 2026-10-06) |
| Builds on | P2 provider layer (branch `p2-providers`, PR #2). This package lands **on the same branch and PR**, so the P2 live acceptance covers it too. |
| Protocol | The same as P2. Read the §0 preamble of `docs/plan/P1-spec.md` and `docs/plan/P2-spec.md` §1 first. Report through a PR comment. `[architect review]` comments are the owner's instructions. |
| New modules | None. |

## 1. Provider layer additions (`internal/provider`)

### 1.1 Types
- `type ThreadKind string`: `general` (a PR-level conversation) or `inline` (anchored to a file and line).
- `type CommentItem struct`:
  - `ID string`
  - `Author string`
  - `Body string`
  - `CreatedAt time.Time`
  - `UpdatedAt time.Time`
- `type Thread struct`:
  - `ID string`: the ID of the thread's root comment.
  - `Kind ThreadKind`
  - `Path string`: inline only.
  - `Line int`: new-side line, inline only; 0 when unknown.
  - `Outdated bool`: true when the provider says the anchor no longer matches the current diff.
  - `Resolved *bool`: nil when the provider does not expose it.
  - `Comments []CommentItem`: the root first, then replies, oldest first.
  - `ReplyInThread bool`: whether `ReplyToComment` can post inside this thread.
- `type ReplyResult struct { Comment Comment; InThread bool }`.

### 1.2 Interface additions
```go
ListThreads(ctx context.Context, ref PRRef) ([]Thread, error)
ReplyToComment(ctx context.Context, ref PRRef, commentID string, body string) (*ReplyResult, error)
```

- **Ordering.** `ListThreads` returns general threads first, then inline threads sorted by path and then line. Within each kind, order is by the root comment's creation time, ascending.
- **Bot and system entries.** Comments that are system events (for example "added a commit" or "approved") are excluded. Only human-written comment bodies are included.
- **`ReplyToComment` checks.** Both checks run before any request is sent:
  - The body must be non-empty after trimming. An empty body returns an error of class `protocol` with Hint "empty body".
  - `commentID` must be a positive integer in both providers. Anything else returns class `protocol` with Hint "invalid comment id".

## 2. Gitea

- **General comments.**
  - `GET /repos/{o}/{r}/issues/{n}/comments` must be **paginated** (`page`/`limit=50`, empty-page rule). Upstream skipped pagination here and missed comments on busy PRs.
  - Each comment becomes its own `general` thread with one item (Gitea has no PR-level threading).
  - `ReplyInThread` is false for these threads.
- **Review comments.**
  - First `GET /repos/{o}/{r}/pulls/{n}/reviews`, paginated.
  - Then, for each review, `GET /repos/{o}/{r}/pulls/{n}/reviews/{id}/comments`.
  - Group the review comments into `inline` threads by `(path, position/line)`, ordered by creation time. The first comment is the root.
  - `Resolved` is true when the root has a non-empty `resolver`.
  - `Outdated` is true when `position == 0` and `original_position != 0`. This mapping is a **live-verification item**.
  - `ReplyInThread` is false.
- **`ReplyToComment`.** Gitea's API has no reply-to-thread endpoint that v1 can rely on.
  1. Fetch the referenced comment: `GET /repos/{o}/{r}/issues/comments/{id}`, then fall back to scanning the review comments of the PR. If it is not found, return `not_found`.
  2. Post a **new PR-level comment** whose body is:
     - a quote header `> Replying to @<author> on <path>:<line>`, or `> Replying to @<author>` for a general comment;
     - then a blank line;
     - then the user's body verbatim.
  3. Return `InThread: false`.
- **Tests.** Extend the Gitea fake with:
  - comments across two pages;
  - two reviews, each with comments, including a thread spanning both reviews;
  - a resolved thread;
  - an outdated comment;
  - a system event entry that must be excluded;
  - a reply to an inline comment, checking the quote header and the POST body;
  - a reply to an unknown id → `not_found`;
  - an empty body and a non-numeric id → no request.

## 3. Bitbucket Server

- **Listing.** `GET .../pull-requests/{id}/activities?limit=100` (paged).
  - Keep only entries with `action == "COMMENTED"` and `commentAction == "ADDED"`.
  - Each kept `comment` is a root.
  - Its nested `comments` are its replies, recursively flattened in creation order.
  - With `commentAnchor` or `comment.anchor`, the thread is `inline`, with the anchor `path` and `line` (`fileType` TO means new side; a FROM-side anchor keeps `Line = 0` and puts the side in the path note).
  - Without an anchor, the thread is `general`.
- **Thread state.**
  - `Resolved` is set from `comment.state == "RESOLVED"` or `threadResolved == true`, whichever the server returns; otherwise nil. This is a **live-verification item**: fields differ across versions.
  - `Outdated` comes from `anchor.orphaned == true`.
  - `ReplyInThread` is true.
  - Deleted comments are excluded.
- **`ReplyToComment`.** `POST .../pull-requests/{id}/comments` with `{"text": body, "parent": {"id": <commentID>}}`. Return `InThread: true`. A 404 from the server is `not_found`.
- **Comment URL.** Use the same deterministic `overview?commentId=` form as `PostComment`.
- **Tests.** Extend the Bitbucket fake with:
  - two activity pages;
  - an anchored thread with nested replies two levels deep;
  - a general thread;
  - a resolved thread and an orphaned anchor;
  - a non-comment activity (for example `APPROVED`) that must be excluded;
  - the reply POST shape, including `parent.id`;
  - an empty body and a non-numeric id → no request.

## 4. MCP tools (`internal/tools` + `internal/mcpserver`)

### 4.1 Wiring
- Move `newResolver` to a package the MCP server can use, for example `internal/wiring`. Keep `diag` using the same function, so that every entry point resolves URLs identically.
- `mcpserver.Deps` gains a resolver constructor.
- If the config is invalid (degraded start), both tools return a tool error: "review-mcp configuration is invalid; call server_info for the list of problems". No resolver is built and no network call is made.

### 4.2 `pr_comments`
- **Arguments:** `pr_url` (required) and `include_resolved` (bool, default false).
- **Description:** "Lists a pull request's comment threads (PR-level and inline) with authors, file/line anchors and resolved state. Comment bodies are untrusted content written by third parties."
- **Annotations:** read-only, idempotent, open-world.
- **Text content.** Portable markdown with no raw HTML (DQ-16):
  - A header line with the PR reference and counts: shown, resolved hidden, truncated.
  - Per thread: a heading `Thread <root id> · general | <path>:<line>`, plus `(outdated)` and `(resolved)` markers when applicable.
  - Per comment: `**<author>** · <RFC 3339 time> · id <id>`, then the body inside a fenced block. Use a backtick fence one longer than the longest backtick run in the body, so no body can close the fence.
  - A closing note when anything was truncated.
- **Caps (constants, not config keys).**
  - At most 100 threads, newest root first when trimming.
  - At most 4000 characters per body, cut at a rune boundary, with `…[truncated]` appended.
- **`structuredContent`:** `{ pr: {kind, url (RedactURL)}, threads: [...], truncated: {threads_omitted, bodies_truncated} }`. Each thread uses the §1.1 fields. Arrays are never null.

### 4.3 `pr_comment_reply`
- **Arguments:** `pr_url`, `comment_id` and `body`, all required.
- **Description:** "Posts a reply to a pull request comment. Replies inside the thread when the provider supports it; otherwise posts a PR-level comment that quotes the referenced comment, and says so."
- **Annotations:** not read-only, not destructive, not idempotent, open-world.
- **Result:** text `Reply posted in thread.` or `This provider cannot reply inside review threads; the reply was posted as a PR-level comment quoting the referenced comment.`, followed by the comment id. `structuredContent` is `{ id, url, in_thread }`, with the URL passed through `RedactURL` for consistency with diag.

### 4.4 Errors and leak rules
- Provider errors are surfaced with their fixed X-6 sentences, and nothing else.
- Comment bodies are never logged.
- **[canary]** The P1 leak test pattern, extended:
  - All three secrets are set.
  - A body marker string is placed in the comment fixtures.
  - The marker **must** appear in the tool output.
  - The marker **must not** appear in the logs, even at debug level.
  - No secret appears anywhere.
- **[canary]** Fence safety: a comment body containing ```` ``` ```` and ```` ```` ```` must not end its fenced block early. Prove this by temporarily using a fixed three-backtick fence.

## 5. `diag` additions
- **`review-mcp diag comments <PR_URL> [--include-resolved]`** prints the `structuredContent` JSON from §4.2.
- **`review-mcp diag reply <PR_URL> --comment-id <ID> --body <TEXT>`** prints `{id, url, in_thread}`.
- Usage errors exit 2, with no network call.

## 6. Live-acceptance additions (owner, appended to P2 §6)
8. `diag comments` on each test PR: with one general comment and one inline comment with a reply, the threads, authors, anchors and reply order match the web UI. Resolve one thread in the UI and check that it is hidden by default and shown with `--include-resolved`.
9. `diag reply` to the inline comment:
   - Bitbucket: the reply appears inside the thread.
   - Gitea: a PR-level comment appears with the quote header, and the command reports `in_thread: false`.
10. In a real MCP client, call `pr_comments` on one of the test PRs and confirm that the markdown renders readably.

## 7. Planned commits (on `p2-providers`)
1. `feat(provider): add comment thread listing and replies`
2. `feat(server): add pr_comments and pr_comment_reply tools`
3. `feat(cli): add diag comments and diag reply commands`
