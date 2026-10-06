"""Write the prompt-golden cases of internal/review (case.json per case).

Usage: python3 -I make_cases.py <repo>/internal/review/testdata/prompts

Each case holds the variables both renderers need: the X-4 toggles, the
max findings, the extra instructions, the output language and the PR fields.
The PR sample is synthetic. The diff is a short numbered (decoupled) diff in
the format internal/patch renders; its exact bytes do not matter for the
prompt comparison because both renderers embed it verbatim.
"""

import json
import os
import sys

DIFF = """

## File: 'internal/httpx/retry.go'

@@ -10,6 +10,12 @@ func Do(c *Client, req *Request) error {
__new hunk__
10  	var lastErr error
11 +	for attempt := 0; attempt < c.maxRetries; attempt++ {
12 +		resp, err := c.send(req)
13 +		if err == nil {
14 +			return resp.Body.Close()
15 +		}
16 +		lastErr = err
17 +	}
18  	return lastErr
__old hunk__
 	var lastErr error
-	resp, err := c.send(req)
-	_ = resp
 	return lastErr"""

PR = {
    "title": "Retry failed HTTP requests",
    "branch": "feature/http-retry",
    "description": "Adds a bounded retry loop to the HTTP client.\n\nCloses the body on success.  \n",
    "date": "2026-10-06",
    "diff": DIFF,
}

ALL = {"effort": True, "tests": True, "security": True}
NONE = {"effort": False, "tests": False, "security": False}
EXTRA = "Focus on error handling.\nIgnore formatting-only changes."

CASES = {
    "all_fields": dict(toggles=ALL),
    "all_fields_extra": dict(toggles=ALL, extra_instructions=EXTRA),
    "key_issues_only": dict(toggles=NONE),
    "key_issues_only_extra": dict(toggles=NONE, extra_instructions=EXTRA),
    "non_english": dict(toggles=ALL, language="tr-TR"),
    "non_english_extra": dict(toggles=ALL, language="de-DE", extra_instructions=EXTRA),
    "tests_and_security_no_description": dict(
        toggles={"effort": False, "tests": True, "security": True}, description=""),
    "effort_only_max_findings_5": dict(
        toggles={"effort": True, "tests": False, "security": False}, max_findings=5),
}


def main():
    out = sys.argv[1]
    for name, spec in CASES.items():
        case = {
            "toggles": spec["toggles"],
            "max_findings": spec.get("max_findings", 3),
            "extra_instructions": spec.get("extra_instructions", ""),
            "language": spec.get("language", "en-US"),
        }
        for k, v in PR.items():
            case[k] = spec.get(k, v)
        d = os.path.join(out, name)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "case.json"), "w", encoding="utf-8") as f:
            json.dump(case, f, indent=2, ensure_ascii=False)
            f.write("\n")


if __name__ == "__main__":
    main()
