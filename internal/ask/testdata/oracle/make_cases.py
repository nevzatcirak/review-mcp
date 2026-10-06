"""Write the prompt-golden cases of internal/ask (case.json per case).

Usage: python3 -I make_cases.py <repo>/internal/ask/testdata/prompts

Each case holds the variables both renderers need: the extra instructions,
the output language, the main PR language (the first diffpipe language
group; "" or "Other" omits the line), the PR fields, the question and the
diff. The PR sample is synthetic. The diff is a short plain diff in the
format internal/patch renders; its exact bytes do not matter for the prompt
comparison because both renderers embed it verbatim.
"""

import json
import os
import sys

DIFF = """## File: 'internal/httpx/retry.go'

@@ -10,6 +10,12 @@ func Do(c *Client, req *Request) error {
 	var lastErr error
+	for attempt := 0; attempt < c.maxRetries; attempt++ {
+		resp, err := c.send(req)
+		if err == nil {
+			return resp.Body.Close()
+		}
+		lastErr = err
+	}
-	resp, err := c.send(req)
-	_ = resp
 	return lastErr"""

PR = {
    "title": "Retry failed HTTP requests",
    "branch": "feature/http-retry",
    "description": "Adds a bounded retry loop to the HTTP client.\n\nCloses the body on success.  \n",
    "question": "Does the retry loop close the response body on every attempt?",
    "main_language": "Go",
    "diff": DIFF,
}

EXTRA = "Focus on error handling.\nIgnore formatting-only changes."

CASES = {
    "base": {},
    "extra": dict(extra_instructions=EXTRA),
    "no_main_language": dict(main_language=""),
    "other_main_language": dict(main_language="Other"),
    "no_description": dict(description=""),
    "non_english": dict(language="tr-TR"),
    "non_english_extra": dict(language="de-DE", extra_instructions=EXTRA),
    "multiline_question": dict(
        question="Two things:\n\n1. Is the loop bounded?\n2. What happens to lastErr when\n   the last attempt fails?\n"),
}


def main():
    out = sys.argv[1]
    for name, spec in CASES.items():
        case = {
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
