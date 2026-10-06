# P2 — Provider Layer: Specification

| | |
|---|---|
| Phase | P2 (phase-plan.md) |
| Work packages | WP-PR-2a (provider core: types, errors, URL resolver, HTTP client, git diff parser) · WP-PR-2b (Gitea) · WP-PR-2c (Bitbucket Server) · WP-PR-2d (`diag` CLI + docs) |
| Binding inputs | `docs/design/v1-design-decisions.md`: DQ-16, DQ-17, DQ-19, DQ-20, DQ-21, X-1, X-2, X-6, X-8, §5. Background: `docs/research/pr-agent-porting-map.md` §F (and §A for file-type handling). |
| Builds on | P1 (`internal/config`, `internal/logging`, `cmd/review-mcp`), merged at `188d99c`. |
| Execution | Sequential 2a → 2b → 2c → 2d. The P1 protocol applies unchanged: Sonnet subagents implement, the lead reviews against §5 of this spec, every commit is verified green in isolation, and each package is reported as a PR comment on the P2 PR. Comments starting with `[architect review]` are the owner's instructions. |
| Acceptance | Hermetic tests use `httptest` fake servers. Live acceptance by the owner uses `review-mcp diag` against one real Gitea PR and one real Bitbucket Server PR (§6). |

The §0 preamble of `docs/plan/P1-spec.md` (rules 1–9) applies to every work package in this phase. Read it first.

---

## 1. WP-PR-2a — Provider core

### 1.1 Package layout
```
internal/provider/          types, Provider interface, errors, Resolver
internal/provider/httpx/    shared HTTP client (auth injection, TLS, caps, redirect policy, logging)
internal/gitdiff/           git unified-diff parser (used by Gitea; reusable)
```
Provider implementations go in `internal/provider/gitea` (2b) and `internal/provider/bitbucketserver` (2c). Only the provider packages and `httpx` perform network I/O.

### 1.2 Types (`internal/provider`)
- `type Kind string` with constants `KindGitea = "gitea"` and `KindBitbucketServer = "bitbucket_server"`. These strings are already used in `server_info` and must stay identical.
- `type PRRef struct { Kind Kind; Namespace string; Repo string; Number int64; URL string }`. `Namespace` is the Gitea owner or the Bitbucket project key (`~user` for personal repositories). `URL` is the user-supplied PR URL; it may only be logged through `logging.RedactURL`.
- `type PullRequest struct { Title, Description, Author, SourceBranch, TargetBranch, HeadSHA, BaseSHA, WebURL string; State string }`. `BaseSHA` is the revision the diff was computed against (see 2b and 2c).
- `type ChangeType string` with values `added`, `modified`, `deleted`, `renamed`. A rename with content changes is `renamed`; the hunks are still present.
- `type ContentStatus string` with values `full`, `not_fetched_file_limit`, `not_fetched_size_limit`, `fetch_failed`, `not_applicable` (deleted head side or added base side).
- `type FilePatch struct`:
  - `Path` (new path; for a deletion, the old path)
  - `OldPath` (set only for a rename)
  - `Type ChangeType`
  - `Patch string`: a **hunk-only** unified diff, starting at the first `@@` line, with no `diff --git`, `index`, `---` or `+++` lines; `\ No newline at end of file` lines are kept verbatim
  - `Additions int`, `Deletions int`, `Binary bool`
  - `BaseContent`, `HeadContent *string`: nil when not fetched
  - `BaseStatus`, `HeadStatus ContentStatus`
- `type SkippedFile struct { Path string; Reason string }`. Reason values: `binary`, `file_limit`, `size_limit`, `fetch_failed`, `filtered`.
- `type Diff struct { Files []FilePatch; Skipped []SkippedFile; BaseStrategy string }`. `BaseStrategy` is one of: `gitea:merge_base`, `gitea:base_sha`, `bbs:merge_base_endpoint`, `bbs:ancestor_walk`.
- `type DiffOptions struct { Include func(path string) bool }`. When non-nil, a file for which `Include` returns false goes to `Skipped` with reason `filtered`, **before any content is fetched**. P3 plugs its ignore and bad-extension rules into this hook. P2 passes nil.
- `type Comment struct { ID string; URL string }`.
- `type Capabilities struct { GFM, MarkdownTables, Labels, InlineComments bool }` (DQ-16). Gitea: `{true, true, true, true}`. Bitbucket Server: `{false, true, false, true}`. v1 does not use labels or inline comments; the fields exist only for later phases.

### 1.3 Interface
```go
type Provider interface {
    Kind() Kind
    Capabilities() Capabilities
    GetPullRequest(ctx context.Context, ref PRRef) (*PullRequest, error)
    GetCommitMessages(ctx context.Context, ref PRRef) ([]string, error) // oldest first
    GetDiff(ctx context.Context, ref PRRef, pr *PullRequest, opts DiffOptions) (*Diff, error)
    PostComment(ctx context.Context, ref PRRef, body string) (*Comment, error)
    FileLineURL(ref PRRef, pr *PullRequest, path string, line int) string // pure, no I/O
}
```
- Providers are cheap per-request values. They are built from `config.Config` plus the provider's `config.Secret`. The secret is revealed only at the moment the header is set and is never stored in its revealed form (decision 6; `serve` mode will build providers per request).

### 1.4 Errors (X-6)
- `type Error struct { Class ErrorClass; Status int; Hint string }` with `Error()` returning a **fixed sentence per class**, plus the HTTP status when there is one, plus `Hint` (a config key or env var name). The error never includes response bodies, URLs with query strings, or header values.
- Classes:
  - `url_not_configured`: the PR URL matches no enabled provider
  - `url_malformed`: the PR URL matches a provider but is not a PR URL of that provider
  - `auth`: 401/403
  - `not_found`: 404
  - `rate_limited`: 429
  - `upstream`: 5xx
  - `too_large`: a cap was exceeded; the Hint names the cap key
  - `unsupported_version`
  - `transport`: DNS, TLS, timeout or connection errors; the Hint gives a short class only, such as "TLS verification failed" or "timeout"
  - `protocol`: an unexpected response shape
- Support `errors.Is` by class sentinel values: `provider.ErrAuth`, `provider.ErrNotFound`, and so on.
- **[canary]** For every class, a test asserts that `Error()` contains none of the following: the token, the response body marker string served by the fake server, or a query-string value.

### 1.5 Resolver (X-2) — `func NewResolver(cfg *config.Config) *Resolver` and `func (r *Resolver) Resolve(rawURL string) (PRRef, Provider, error)`
- Candidates are the enabled providers' `base_url` values, plus Gitea's `web_url` when set.
- A PR URL matches a candidate when **all** of these hold:
  - the scheme is equal (case-insensitive);
  - the host is equal (case-insensitive);
  - the effective port is equal, with defaults 80/443 applied;
  - the candidate path is a prefix of the PR URL path **at a segment boundary**: `https://h/bb` matches `https://h/bb/projects/...` but not `https://h/bbx/...`.
- When several candidates match, the **longest path prefix** wins.
- The remainder path after the prefix is parsed by the provider (`ParsePRPath`, see 2b and 2c). A query string and fragment on the PR URL are ignored.
- No match → `url_not_configured`; the Hint lists the configured base URLs (redacted). A match whose remainder does not parse → `url_malformed`.
- **No network I/O happens in `Resolve`.**
- **[canary]** A PR URL on a foreign host, on a look-alike host (`your-gitea.example.evil.com`), on a different port, on a different scheme, and on a sibling path prefix (`/bbx`) is rejected, and the fake server registers **zero** requests.

### 1.6 HTTP client (`internal/provider/httpx`)
- `func New(opts Options) (*Client, error)` with these options: `BaseURL` (normalized), `Auth func(*http.Request)` (the provider-specific header setter), `CACertPath`, `InsecureSkipVerify`, `UserAgent` (`review-mcp/<version>`) and `Logger`.
- **TLS:**
  - The system roots are used, plus `CACertPath` when it is set.
  - `InsecureSkipVerify` is honoured only when configured. The config layer already warns about it.
- **Timeouts:** each request uses a 60 s timeout. This is a constant, not a config key (keeping §5 unchanged). Context cancellation is respected.
- **Redirects:** a redirect is followed only when the target stays under the same scheme, host, port and base path. Any other redirect is an error of class `protocol`, raised without following it. The Authorization header is set by `Auth` on each request, never copied by hand.
  - **[canary]** The fake server redirects to a second fake server. The second server receives no request at all, and in particular no Authorization header.
- **Response caps:**
  - JSON bodies: a constant 10 MiB.
  - Raw file content: `diff.max_file_bytes`.
  - Whole diffs: `diff.max_diff_bytes`.
  - Bodies are read through a limit of `cap+1` bytes. Overflow → `too_large`, with a Hint naming the key.
  - **[canary]** A body of exactly the cap is accepted; one byte more is rejected.
- **Status mapping** to the §1.4 classes is done in one function shared by all providers.
- **Logging (X-8):** each request is logged at debug level with method, `logging.RedactURL(url)`, status and duration. Never logged: headers, request bodies, response bodies.
  - **[canary]** A debug-level run of every provider test captures the log, which contains neither the token nor any fake response body marker.
- **Pagination helpers:**
  - `PagesUntilEmpty` (Gitea): keep requesting `page=1,2,…` until a page comes back **empty**. A short page is not the end.
  - `PagesStartLimit` (Bitbucket Server): `start`, `limit`, `isLastPage`, `nextPageStart`.
  - Both enforce a hard maximum of 1000 pages; exceeding it → `protocol`.
  - **[canary]** Gitea: the fake server returns a short page followed by a non-empty page. All items must be collected.

### 1.7 Git diff parser (`internal/gitdiff`)
- `func Parse(r io.Reader) ([]File, error)` for `git diff` output (the Gitea `.diff` endpoint). This is our own implementation (DQ-17); no new dependency.
- Supported per file:
  - `diff --git a/X b/Y` headers;
  - `old mode`/`new mode`, `new file mode`, `deleted file mode`;
  - `similarity index`, `rename from`/`rename to`, `copy from`/`copy to` (a copy is treated as `added`);
  - `index` lines;
  - `---`/`+++` with `/dev/null`;
  - `Binary files … differ` and `GIT binary patch` (binary, no hunks);
  - quoted paths with C-style and octal escapes: unquote with `strconv.Unquote` semantics, and reject on failure;
  - `\ No newline at end of file`;
  - CRLF content lines, which are preserved byte-exact.
- Output per file: paths, change type, binary flag, the hunk-only patch text (byte-exact slice from the first `@@` to the end of the file's section), and addition/deletion counts.
- Paths are taken from `rename to`, `+++ b/` or `--- a/` (for deletions), in that order of preference. The `diff --git` line is a fallback only, because it is ambiguous with spaces.
- Mode-only changes (no hunks) are reported as `modified` with an empty patch.
- **Tests:** golden, table-driven cases for every item above, including:
  - a path containing ` b/`;
  - a path with a space;
  - a unicode path (octal-escaped);
  - a rename without content change;
  - a rename with content change;
  - a deleted file;
  - a new empty file;
  - a binary file;
  - a mode-only change;
  - two files where the first lacks a trailing newline.
- **[canary]** For the ` b/`-in-path case, prove that a naive `split(" b/")` implementation (upstream's approach) fails the test.

---

## 2. WP-PR-2b — Gitea provider (`internal/provider/gitea`)

- **Auth:** `Authorization: token <PAT>`. All API paths are under `{base_url}/api/v1`.
- **`ParsePRPath(remainder)`:** accepts `/{owner}/{repo}/pulls/{n}`, optionally followed by more segments (`/files`, `/commits`). Owner and repo must be non-empty and are path-unescaped. `n` must be a positive integer.
- **Endpoints** (the porting map §F.2 table):

  | Purpose | Method and path |
  |---|---|
  | PR | `GET /repos/{o}/{r}/pulls/{n}` |
  | Whole diff | `GET /repos/{o}/{r}/pulls/{n}.diff` (cap `diff.max_diff_bytes`) |
  | Changed-file metadata | `GET /repos/{o}/{r}/pulls/{n}/files?page=&limit=50` (empty-page rule) |
  | Commits | `GET /repos/{o}/{r}/pulls/{n}/commits?page=&limit=50` (newest first; reverse to oldest first) |
  | Raw content | `GET /repos/{o}/{r}/raw/{path}?ref={sha}` (path segments escaped individually) |
  | Comment | `POST /repos/{o}/{r}/issues/{n}/comments` with `{"body": …}` |

- **Base revision:**
  - Use the PR's `merge_base` field when it is non-empty (`BaseStrategy = gitea:merge_base`). Otherwise use `base.sha` (`gitea:base_sha`).
  - Rationale: the `.diff` endpoint compares the merge base with head, so base content must come from the same revision. **Live-verification item** (§6).
- **Diff assembly:**
  1. Parse the `.diff` with `internal/gitdiff`.
  2. Join it with the `/files` metadata by path (new path, or old path for deletions). If a file appears in one source but not the other, log it at debug level. A file present only in the diff is kept; a file present only in `/files` goes to `Skipped` with reason `fetch_failed`.
  3. Apply `DiffOptions.Include`.
  4. For non-binary files, fetch contents in API order with bounded concurrency (4):
     - The first `diff.max_files_full_content` files get contents.
     - Later files get status `not_fetched_file_limit` but keep their patch.
     - A content larger than `diff.max_file_bytes` gets `not_fetched_size_limit` and keeps its patch.
     - A 404 or other per-file error gets `fetch_failed` and keeps its patch.
     - Content fetching never fails the whole diff; only the PR, `.diff` and `/files` requests can.
  5. Binary files go to `Skipped` with reason `binary`.
- **`FileLineURL`:** `{web_url or base_url}/{o}/{r}/src/commit/{head_sha}/{escaped path}#L{line}`. **Live-verification item.**
- **Tests:** an `httptest` fake Gitea serving synthetic fixtures (generic names, placeholder hosts). Cover:
  - PR fetch;
  - diff join;
  - rename via `/files` + diff;
  - the content caps (file count and size) with the correct statuses;
  - a per-file 404 → `fetch_failed`, with the patch kept;
  - commit order reversal;
  - comment POST body shape;
  - auth header shape;
  - error class mapping for 401/403/404/429/500.

---

## 3. WP-PR-2c — Bitbucket Server provider (`internal/provider/bitbucketserver`)

- **Auth:** `Authorization: Bearer <token>` only (DQ-21).
- **Base URL:** the base URL includes the context path and comes **only from config** (X-2). REST paths are `{base_url}/rest/api/1.0/...`; the merge-base endpoint uses `/rest/api/latest/...`.
- **`ParsePRPath(remainder)`:** accepts the following, optionally followed by more segments (`/overview`, `/diff`):
  - `/projects/{KEY}/repos/{slug}/pull-requests/{id}`
  - `/users/{user}/repos/{slug}/pull-requests/{id}`, which becomes project key `~{user}`.
- **Version probe (DQ-20):** `GET /rest/api/1.0/application-properties`.
  - If the probe succeeds and the major version is below 7, return `unsupported_version` before any other call.
  - If the probe fails for any reason, log it at debug level and continue.
- **Endpoints:**

  | Purpose | Method and path (under `/rest/api/1.0/projects/{K}/repos/{s}` unless noted) |
  |---|---|
  | PR | `GET /pull-requests/{id}` |
  | Changes | `GET /pull-requests/{id}/changes` (paged) |
  | PR commits | `GET /pull-requests/{id}/commits` (paged; newest first; reverse to oldest first) |
  | Merge base | `GET {base}/rest/api/latest/projects/{K}/repos/{s}/pull-requests/{id}/merge-base` |
  | Destination history (ancestor walk) | `GET /commits?since={a}&until={b}` (paged) |
  | Raw content | `GET /raw/{path}?at={sha}` (path segments escaped individually) |
  | Comment | `POST /pull-requests/{id}/comments` with `{"text": …}` |

- **Base revision (DQ-20):**
  - Call the merge-base endpoint. On 200, `BaseStrategy = bbs:merge_base_endpoint`.
  - On 404, use the ancestor walk (`bbs:ancestor_walk`). Implement it with the **same semantics as upstream's `get_best_common_ancestor`** at the pinned revision (porting map §F.3). Read the upstream function for exact behaviour and implement it independently, without copying code.
  - The naive first-parent strategy is **not** ported.
  - Any other error fails `GetDiff`: a wrong base is worse than no review.
- **Diff assembly (DQ-19):**
  1. Take the changes list in API order.
  2. Apply `DiffOptions.Include`.
  3. The first `diff.max_files_full_content` files are processed; later files go to `Skipped` with reason `file_limit`. Bitbucket Server cannot produce a patch without contents.
  4. For each processed file, fetch base content at `BaseSHA` (at `srcPath` for renames) and head content at `HeadSHA`, with bounded concurrency (4).
     - A side larger than `diff.max_file_bytes` → `Skipped` with `size_limit`.
     - A fetch error → `Skipped` with `fetch_failed`.
     - A NUL byte in the first 8000 bytes of either side → `Skipped` with `binary`.
  5. Generate a unified diff with **`github.com/pmezard/go-difflib`** (BSD-3-Clause; frozen but widely used; a port of Python difflib, giving parity with upstream's `difflib`), 3 context lines. Strip the `---`/`+++` header lines to get a hunk-only patch.
     - A file whose contents are identical on both sides is dropped silently. This is not a skip, because there is no change.
  6. Compute addition and deletion counts from the generated patch.
- **`GetCommitMessages`:** implemented from the PR commits endpoint. Upstream left this unimplemented; we do not inherit the gap.
- **`FileLineURL`:** `{base_url}/projects/{K}/repos/{s}/pull-requests/{id}/diff#{escaped path}?t={line}`. **Live-verification item:** the anchor format differs between Bitbucket versions. If it cannot be verified, fall back to the PR diff page URL without a line anchor, and say so in the report.
- **Tests:** an `httptest` fake Bitbucket Server under a context path (`/bitbucket`). Cover:
  - the version probe: below 7 → error; probe failure → continue;
  - merge-base 200;
  - merge-base 404 → ancestor walk on a synthetic history;
  - a rename (base content fetched at the old path);
  - the file limit;
  - the size limit;
  - binary detection;
  - identical contents dropped;
  - paging via `nextPageStart`;
  - the comment POST shape;
  - the Bearer header;
  - the error class mapping.

  **[canary]** Ancestor walk: a history where the PR's oldest parent is *not* the merge base (the target branch moved after the PR branched) must yield the true common ancestor. Prove that a first-parent-only implementation fails this test.

---

## 4. WP-PR-2d — `diag` CLI and docs

Purpose: a connectivity and acceptance tool for users ("can review-mcp see my PR?"), and the vehicle for P2 live acceptance. It is not an MCP tool. Like `version`, it may write to stdout.

- **`review-mcp diag pr <PR_URL> [--show-patch <path>]`:**
  1. Load the config. If it is invalid, print the problems to stderr and exit 1.
  2. Resolve the provider, then fetch the PR, the commit messages and the diff (with `Include` set to nil).
  3. Print a JSON report to stdout containing:
     - `kind`, `ref`: the PR reference;
     - `title`, `source_branch`, `target_branch`, `head_sha`, `base_sha`, `base_strategy`: the PR details and how the base was found;
     - `commit_count`, and the first line of each commit message;
     - `files`: per file, `path`, `old_path`, `type`, `additions`, `deletions`, `patch_bytes`, `base_status`, `head_status`, `binary`;
     - `skipped`: the skipped files with their reasons;
     - `totals`;
     - `elapsed_ms`.
  4. With `--show-patch <path>`, print that one file's hunk-only patch after the JSON, separated by a line `--- patch: <path> ---`.
  5. Exit 0 on success. On error, print the X-6 error to stderr and exit 1.
- **`review-mcp diag comment <PR_URL> --body <TEXT>`:**
  - Post one PR-level comment and print `{"id": …, "url": …}`.
  - The body is required. Nothing is added to it.
- **Leak guarantee:** neither command prints a secret, an Authorization header or a URL with userinfo.
  - **[canary]** An e2e test runs both commands against the fake servers with debug logging. It scans stdout and stderr for the token and for a fake response-body marker that must never be echoed.
- **Docs:**
  - Add `docs/troubleshooting.md`: how to use `diag pr`, what each error class means, and what to check (token scopes, base URL including context path, CA certificates).
  - Add one line in `getting-started.md` linking to it.
  - Token scopes needed, stated generically:
    - Gitea: read access to the repository plus write access to issues/PRs, the latter only for `publish`/`diag comment`;
    - Bitbucket Server: an HTTP access token with repository read, plus write only for comments.

---

## 5. Architect review checklist (per package, before commit)

- Read the full diff. Re-run the gates. Re-prove at least one canary independently.
- 2a: check §1.5 against X-2 point by point, and check that no request is ever built from user-supplied host data.
- 2b/2c: check the endpoints against the §2/§3 tables. Check that the token is revealed only inside `Auth`.
- No new modules other than `github.com/pmezard/go-difflib` (2c).
- Resolve DESIGN-QUESTIONs before committing. Anything that changes a design decision goes to the architect via a PR comment, and work on that point waits for the answer.

## 6. Live acceptance (owner)

Use instances that belong to you personally, or throwaway local ones. Nothing about the instances (hosts, project names, PR content) goes into the repository, the PR or the commit messages; transcripts in the PR must be redacted to placeholder hosts. Suggested setup: Gitea via its official container image locally; Bitbucket Data Center via its official container image with an evaluation license.

For one Gitea PR and one Bitbucket Server PR, each containing at least one modified file, one added file, one deleted file and one renamed file with edits:

1. `review-mcp diag pr <url>`: the file list, types, rename paths and +/- counts match the provider's web UI. `base_strategy` is reported.
2. `--show-patch` on the renamed file: the hunks match the UI diff (line numbers and content).
3. Gitea: `base_strategy` is `gitea:merge_base`. Create the PR after advancing the target branch, so that the merge base differs from the target head, and check that the diff still matches the UI.
4. Bitbucket Server: record which strategy was used and the server version, then check the diff against the UI.
5. `review-mcp diag comment <url> --body "review-mcp connectivity check"`: the comment appears on the PR.
6. A PR URL on a host that is not configured is rejected immediately, with no network wait.
7. Open one `FileLineURL` per provider, taken from a unit test or a debug print, and record whether it lands on the right file and line. If it does not, fix it in P4, where links are first shown to users.

Results are posted on the P2 PR as a redacted acceptance record. The merge happens after this record exists.

## 7. Planned commits (branch `p2-providers`)

1. `feat(gitdiff): add git unified-diff parser`
2. `feat(provider): add provider types, error classes, URL resolver and HTTP client`
3. `feat(gitea): add Gitea provider`
4. `feat(bitbucketserver): add Bitbucket Server provider`
5. `feat(cli): add diag pr and diag comment commands`
6. `docs: add troubleshooting guide and provider token scopes`
