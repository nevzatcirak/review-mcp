# P8 — Slow-endpoint usability: Specification

| | |
|---|---|
| Phase | P8 (added 2026-10-07 by owner decision; in v1.0 scope) |
| Work packages | WP-PR-8a (context window from the endpoint) · WP-PR-8b (long calls: background jobs) · WP-PR-8c (latency controls, docs) |
| Origin | Owner session on `v1.0.0-rc.2` with a locally hosted model (acceptance record #8). Two findings: (1) `llm.context_window` has to be looked up and typed by hand; (2) a review that takes about 110 s on a local model is killed by the MCP client after about 60 s (`-32001: Request timed out`), although review-mcp itself would have finished. |
| Binding inputs | Decisions doc: DQ-3, DQ-4, X-6, X-8, X-10. P4 and P5 specs (pipelines, progress notifications). |
| Builds on | P7 (`main` at `7ff097b` or later). Branch `p8-slow-endpoints`. |
| Protocol | Same as P7 (local session, container gates, bundle push). One PR comment per package; stop after each one for the architect. No tag pushes. |
| Model guidance | **Opus for 8b** (concurrency, lifetimes, secrets). Sonnet for 8a and 8c. |
| New Go modules allowed | None. |

## 0. Decisions (added to the decisions doc in WP-PR-8c)

- **X-15 — Context window from the endpoint (amends DQ-3).**
  - `llm.context_window` becomes **optional**.
  - When it is unset, review-mcp asks the endpoint once per process: `GET {llm.base_url}/models`, then the entry whose `id` equals `llm.model`.
  - It reads the first of these fields that holds a positive integer: `max_model_len`, `context_length`, `context_window`, `max_context_length`. It uses **90 %** of that value, rounded down, with a minimum of 4096.
  - A set `llm.context_window` always wins.
  - Training-size fields are **never** used: `n_ctx_train`, `meta.n_ctx_train`, and model metadata in general. A server often serves a smaller context than the model was trained with, and that is exactly the mistake that makes prompts truncate silently.
  - If no field is found, the call fails with a fixed sentence that names `llm.context_window`. It never guesses.
- **X-16 — Background jobs for long calls (stdio only).**
  - `pr_review` and `pr_ask` wait at most `wait_seconds` for their result (argument; default `llm.wait_seconds` = 45).
  - If the run is still going, the tool returns a **running** result with a `job_id` and a fixed instruction to call `job_result`. The run continues in the server process.
  - A new tool, `job_result(job_id, wait_seconds?)`, waits again up to `wait_seconds` and returns either the finished result, exactly as the original tool would have returned it, or the running status.
  - This keeps every call under a typical 60 s client timeout, whatever the model speed, and does not depend on any client setting.
  - **serve mode does not use background jobs:** calls stay synchronous, and `wait_seconds` is ignored there. X-10 requires credentials to live no longer than the request; a background job would outlive it. A serve client sets its own timeout.
- **X-17 — Latency controls.**
  - **Timeout default:** `llm.timeout_seconds` rises to **300**. 120 is too short for local models on large prompts.
  - **New optional cap `diff.max_tokens`:** it caps the diff budget below the context window, so that large windows do not imply very slow requests. Files left out are reported in the coverage section as usual (X-3).
  - `diff.max_tokens` is unset by default.

## 1. WP-PR-8a — Context window from the endpoint (X-15)

1. **Config.** `llm.context_window` is no longer required. Validation:
   - If set, it must be ≥ 4096, as now.
   - If unset, the config is still valid.
   - `server_info` reports `context_window: auto (endpoint)` until it is resolved, and `context_window: <n> (endpoint, 90% of <m>)` or `<n> (config)` after.
2. **Resolver (`internal/llm`).** `ResolveContextWindow(ctx) (n int, source string, err error)`.
   - It uses the same client, the same authentication, the same redirect pinning and the same response cap as `Complete`, with a 10 s timeout.
   - It parses the OpenAI list shape `{data:[{id, …}]}` and also accepts a bare array. Unknown fields are ignored.
   - The model `id` must match **exactly**. When there is no match, it fails with a fixed sentence: "the LLM endpoint does not list the configured model; check llm.model".
   - The field order is exactly the one in X-15. Each field may be a JSON number or a numeric string.
   - When no field is present, it fails with: "the LLM endpoint does not report the model's context window; set llm.context_window".
   - Classification follows X-6: auth, not found, transport and timeout errors reuse the existing LLM classes.
3. **Cache.**
   - Once per process per `(base_url, model)`, behind a mutex. Only successes are cached.
   - A failure is not cached, so a later call can succeed after the endpoint comes up.
   - In serve mode the probe runs with the request's LLM key when `llm_key_source = header`. The cached **number** is not a secret and may be shared. The key is never stored.
4. **Use.** Every place that reads `cfg.LLM.ContextWindow` goes through one accessor that resolves it lazily. This covers the budget in `pr_review` and `pr_ask` and `diag review|ask|diff`.
   - The probe runs **after** argument validation and URL resolution, and before any provider I/O. A failing probe sends nothing to the provider.
   - `diag diff` keeps working without any LLM access only when `--context-window N` is given. Add that flag; it overrides config.
5. **Tests.**
   - A table of endpoint shapes:
     - vLLM-like `max_model_len`;
     - OpenRouter-like `context_length`;
     - a string number;
     - a training-only field (must fail);
     - a missing model;
     - an empty list;
     - 401, 404, timeout.
   - A field-precedence test.
   - The 90 % and 4096-floor arithmetic.
   - Cache: one probe for two calls; no cache after a failure.
   - `server_info` before and after resolution.
   - **[canary]** Make the resolver accept `n_ctx_train`; the training-field case must fail.
   - **[canary]** Remove the config-wins check; the override test must fail.

Commit: `feat(llm): resolve the context window from the endpoint when it is not configured`

## 2. WP-PR-8b — Background jobs (X-16; Opus)

1. **Job store (`internal/jobs`).** Process-local, in memory.
   - **Job id:** 128 random bits, base32, prefix `job_`.
   - **Limits:** at most 4 running jobs; a 5th gets the fixed sentence "too many background jobs are running; wait for one to finish". At most 64 kept results.
   - **Expiry:** a finished job is dropped 30 minutes after it finishes. A running job is never dropped.
   - **State:** `running` (with the current progress stage and the elapsed seconds), `done` (holding the tool's complete result: text content and structured content), `failed` (holding the classified user message).
   - **What a job holds:**
     - its own context, which is detached from the MCP request and cancelled only at process shutdown;
     - the per-call scope;
     - the result.
   - **What a job never holds:** credentials beyond the per-call config, which is dropped when the run ends (stdio reads them from env anyway). Nothing is written to disk.
2. **Tool flow (`pr_review`, `pr_ask`).**
   - Arguments are validated as now. A new optional `wait_seconds` integer argument accepts 0–600; config `llm.wait_seconds` defaults to 45 and accepts 0–600.
   - The run starts in a job, and the handler waits for whichever comes first: the result or `wait_seconds`.
   - **Finished in time:** return exactly today's result. No `job_id` appears and nothing changes for fast endpoints.
   - **Not finished:** return `isError: false` with:
     - text: "The review is still running (stage: <stage>, <n> s so far). Call `job_result` with job_id `<id>` to get the result.";
     - structured content: `{status: "running", job_id, stage, elapsed_seconds}`.
     - For `pr_ask` the text says "The answer is still running …".
   - **`wait_seconds = 0`:** always return the running status at once. This is useful for clients that prefer polling.
   - **serve mode:** no job is created. The run is synchronous and `wait_seconds` is ignored. Document this in `docs/serve.md`.
3. **Tool `job_result`.**
   - **Arguments:** `job_id` (required) and `wait_seconds` (optional, same range and default).
   - **Description:** "Returns the result of a long-running pr_review or pr_ask call that answered with a job_id, waiting up to wait_seconds for it to finish."
   - **Annotations:** read-only, idempotent, not open-world.
   - **Result:**
     - `done`: the original tool's full result, unchanged, including the publish outcome;
     - `failed`: the classified error as a tool error;
     - `running`: the same running result again.
     - An unknown or expired id gets "unknown or expired job_id".
   - Not registered in serve mode.
4. **Publishing in a job.** `publish=true` still happens inside the run. A client that never calls `job_result` still gets its comments posted. The tool description says so.
5. **Progress.** While waiting, the handler keeps sending the existing progress notifications when the request carries a progress token. A client that resets its timeout on progress may then never see the running result.
6. **Shutdown.** On SIGINT or SIGTERM, running jobs are cancelled. The stdio server already exits on EOF; jobs do not keep the process alive.
7. **Tests (hermetic, with a fake LLM that blocks until released):**
   - fast run → today's result, no job;
   - slow run → running → `job_result` → done, with a structured result byte-identical to the synchronous one;
   - `wait_seconds = 0`;
   - failure inside a job → `failed` with the fixed sentence;
   - the 5th concurrent job is refused;
   - expiry after the TTL (injected clock);
   - unknown id;
   - publish happens even if `job_result` is never called;
   - serve mode: no `job_result` tool, synchronous behaviour;
   - **`-race`** with 8 concurrent clients polling the same and different jobs.
   - **[canary]** Tie the job context to the request context: the slow-run test must fail, because the job is cancelled when the first call returns.
   - **[canary] leak:** markers in the question and in the PR content; nothing in the logs. Job ids may be logged at debug level; they are not secrets.
8. **Docs.** `docs/review.md` and `docs/ask.md` gain a "slow endpoints" section covering `wait_seconds`, `job_result`, and why the client timeout no longer matters.

Commits:
- `feat(jobs): add an in-memory background job store`
- `feat(server): return a job id for long pr_review and pr_ask calls and add job_result`

## 3. WP-PR-8c — Latency controls, decisions doc, docs (X-17)
1. **Timeout default:** `llm.timeout_seconds` default 120 → 300, in the config table, the docs and the troubleshooting text.
2. **`diff.max_tokens`** (env `REVIEW_MCP_DIFF_MAX_TOKENS`, optional, ≥ 1000).
   - When set, the diff budget is `min(the existing budget, diff.max_tokens)`.
   - The prompt scaffolding and the reserve rules are unchanged.
   - `diag diff` and `diag review --dry-run` report the effective budget and which limit applied.
   - **[canary]:** a cap smaller than the PR puts the omitted files in the coverage section.
3. **Decisions doc:**
   - add X-15, X-16 and X-17 (summary rows and sections);
   - amend DQ-3;
   - add the config rows `llm.wait_seconds` and `diff.max_tokens`;
   - change the `llm.timeout_seconds` default;
   - mark `llm.context_window` optional;
   - add `job_result` to the §6 tool surface.
4. **`docs/setup.md`, LLM section:**
   - context window auto-detection, its fields, and what to do when the endpoint does not report one. Ollama's `/v1/models` does not report the served context: set `llm.context_window` to the `num_ctx` you run with;
   - local-model advice: `diff.max_tokens` (for example 24000) for snappier reviews; leave `wait_seconds` at its default.
5. **README, quick start:** `llm.context_window` is now optional.
6. **CHANGELOG:** add a `1.0.0-rc.3` section.

Commits:
- `feat(config): raise the LLM timeout default and add an optional diff token cap`
- `docs: document endpoint context windows, background jobs and latency controls`

## 4. Architect review checklist
- The usual checklist.
- **Training-size fields** are never used. A failure is never cached.
- **Job lifetime** is independent of the request and bounded by the TTL. Concurrency limits hold under `-race`.
- **serve mode** has no jobs and no `job_result`, so X-10 is untouched.
- **A fast path** returns exactly the old result.

## 5. Live acceptance (appended to `v1-acceptance.md` as section J)
- **J1:**
  - with `llm.context_window` unset against your local endpoint, `server_info` shows the resolved value and its source;
  - with an endpoint that does not report it, the fixed sentence names the key.
- **J2:**
  - in opencode, a review that takes longer than 60 s returns a running status within about 45 s;
  - asking the client model to fetch the result uses `job_result` and gets the full review;
  - with `publish=true`, the comments appear on the PR.
- **J3:** `diff.max_tokens=24000` on a large PR makes the review noticeably faster, and the omitted files are listed in the coverage section.

## 6. Planned commits (branch `p8-slow-endpoints`)
1. `feat(llm): resolve the context window from the endpoint when it is not configured`
2. `feat(jobs): add an in-memory background job store`
3. `feat(server): return a job id for long pr_review and pr_ask calls and add job_result`
4. `feat(config): raise the LLM timeout default and add an optional diff token cap`
5. `docs: document endpoint context windows, background jobs and latency controls`
