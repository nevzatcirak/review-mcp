"""Render upstream PR-Agent's review prompts for every prompt-golden case.

Usage (see README.md):
  PYTHONPATH=<upstream>:<venv site-packages> python3.12 render_upstream.py <cases dir>

For each <cases dir>/<case>/case.json this writes upstream.system.txt and
upstream.user.txt: the [pr_review_prompt] system and user templates of
pr_agent/settings/pr_reviewer_prompts.toml, rendered the way upstream's
TokenHandler renders them (jinja2.Environment(undefined=StrictUndefined)),
with diff_hunk_format from upstream's own render_diff_hunk_format.

Variable mapping (upstream name <- case):
  require_estimate_effort_to_review <- toggles.effort
  require_tests                     <- toggles.tests
  require_security_review           <- toggles.security
  num_max_findings                  <- max_findings
  extra_instructions                <- extra_instructions, plus the output-
                                       language sentence (see below)
  title, branch, description, date, diff <- the same keys
Every other require_* flag is False, related_tickets is empty, there is no
question/answer, no skills or repo context, no AI metadata and no duplicated
examples: the parts review-mcp removes are switched off, so the remaining
differences are the ones the README lists.

Output language: upstream appends a fixed sentence to every tool's
extra_instructions in PRAgent._handle_request (pr_agent/agent/pr_agent.py)
when config.response_language is not en-us. That step is inline in a large
request handler, so it is restated here; the script asserts that both
literals still appear in upstream's source, so a change upstream fails
loudly.
"""

import json
import os
import sys

from jinja2 import Environment, StrictUndefined

import pr_agent
from pr_agent.algo.prompt_fragments import render_diff_hunk_format
from pr_agent.config_loader import get_settings

LANG_PREFIX = "Your response MUST be written in the language corresponding "
LANG_TEXT = ("Your response MUST be written in the language corresponding to locale code: '{lang}'. "
             "This is crucial. Keep schema control values (such as 'No', 'Yes', 'None', 'false') in their "
             "original English form and do not translate them.")
SEPARATOR = "\n======\n\nIn addition, "


def check_upstream_literals():
    path = os.path.join(os.path.dirname(pr_agent.__file__), "agent", "pr_agent.py")
    with open(path, encoding="utf-8") as f:
        src = f.read()
    for lit in (LANG_PREFIX, "This is crucial. ", "Keep schema control values (such as 'No', 'Yes', 'None', ",
                "'false') in their original English form and do not translate them.",
                'separator_text = "\\n======\\n\\nIn addition, "'):
        assert lit in src, f"upstream literal changed: {lit!r}"


def with_language(extra, lang):
    if lang.lower() == "en-us":
        return extra
    text = LANG_TEXT.format(lang=lang)
    if text in str(extra):
        return extra
    if extra:
        return str(extra) + SEPARATOR + text
    return text


def render(case):
    t = case["toggles"]
    variables = {
        "title": case["title"],
        "branch": case["branch"],
        "description": case["description"],
        "language": "Go",
        "diff": case["diff"],
        "num_pr_files": 1,
        "num_max_findings": case["max_findings"],
        "require_score": False,
        "require_tests": t["tests"],
        "require_estimate_effort_to_review": t["effort"],
        "require_risk_assessment": False,
        "require_merge_recommendation": False,
        "require_priority_files": False,
        "require_estimate_contribution_time_cost": False,
        "require_can_be_split_review": False,
        "require_security_review": t["security"],
        "require_todo_scan": False,
        "question_str": "",
        "answer_str": "",
        "extra_instructions": with_language(case["extra_instructions"], case["language"]),
        "skills_context": "",
        "repo_context": "",
        "commit_messages_str": "",
        "custom_labels": "",
        "enable_custom_labels": False,
        "is_ai_metadata": False,
        "diff_hunk_format": render_diff_hunk_format(include_line_numbers=True, include_ai_metadata=False),
        "related_tickets": [],
        "related_tickets_omitted": 0,
        "duplicate_prompt_examples": False,
        "date": case["date"],
    }
    env = Environment(undefined=StrictUndefined)
    s = get_settings()
    system = env.from_string(s.pr_review_prompt.system).render(variables)
    user = env.from_string(s.pr_review_prompt.user).render(variables)
    return system, user


def main():
    check_upstream_literals()
    root = sys.argv[1]
    for name in sorted(os.listdir(root)):
        path = os.path.join(root, name, "case.json")
        if not os.path.isfile(path):
            continue
        with open(path, encoding="utf-8") as f:
            case = json.load(f)
        system, user = render(case)
        for fname, text in (("upstream.system.txt", system), ("upstream.user.txt", user)):
            with open(os.path.join(root, name, fname), "w", encoding="utf-8", newline="") as f:
                f.write(text)


if __name__ == "__main__":
    main()
