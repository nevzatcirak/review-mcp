"""Render upstream PR-Agent's /ask prompts for every prompt-golden case.

Usage (see README.md):
  PYTHONPATH=<upstream>:<venv site-packages> python3.12 render_upstream.py <cases dir>

For each <cases dir>/<case>/case.json this writes upstream.system.txt and
upstream.user.txt: the [pr_questions_prompt] system and user templates of
pr_agent/settings/pr_questions_prompts.toml, rendered the way upstream's
TokenHandler renders them (jinja2.Environment(undefined=StrictUndefined)).

Variable mapping (upstream name <- case), mirroring PRQuestions.__init__:
  title, branch, description, diff    <- the same keys
  language                            <- main_language, "" when it is "Other"
                                         (the omit rule of spec P5 §1.1; upstream
                                         get_main_pr_language never yields it)
  questions                           <- question
  extra_instructions                  <- extra_instructions, plus the output-
                                         language sentence (see below)
  conversation_history, skills_context <- "" (the parts review-mcp removes
                                         are switched off, so the remaining
                                         differences are the ones the README
                                         lists)

Output language: upstream appends a fixed sentence to every tool's
extra_instructions in PRAgent._handle_request (pr_agent/agent/pr_agent.py)
when config.response_language is not en-us. That step is inline in a large
request handler, so it is restated here; the script asserts that the
literals still appear in upstream's source, so a change upstream fails
loudly.
"""

import json
import os
import sys

from jinja2 import Environment, StrictUndefined

import pr_agent
from pr_agent.config_loader import get_settings

LANG_TEXT = ("Your response MUST be written in the language corresponding to locale code: '{lang}'. "
             "This is crucial. Keep schema control values (such as 'No', 'Yes', 'None', 'false') in their "
             "original English form and do not translate them.")
SEPARATOR = "\n======\n\nIn addition, "


def check_upstream_literals():
    path = os.path.join(os.path.dirname(pr_agent.__file__), "agent", "pr_agent.py")
    with open(path, encoding="utf-8") as f:
        src = f.read()
    for lit in ("Your response MUST be written in the language corresponding ", "This is crucial. ",
                "Keep schema control values (such as 'No', 'Yes', 'None', ",
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
    language = case["main_language"]
    if language == "Other":
        language = ""
    variables = {
        "title": case["title"],
        "branch": case["branch"],
        "description": case["description"],
        "language": language,
        "diff": case["diff"],
        "questions": case["question"],
        "conversation_history": "",
        "extra_instructions": with_language(case["extra_instructions"], case["language"]),
        "skills_context": "",
    }
    env = Environment(undefined=StrictUndefined)
    s = get_settings()
    system = env.from_string(s.pr_questions_prompt.system).render(variables)
    user = env.from_string(s.pr_questions_prompt.user).render(variables)
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
