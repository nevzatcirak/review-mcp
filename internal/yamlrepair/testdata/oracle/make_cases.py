"""Write the input fixtures of internal/yamlrepair/testdata/cases.

Usage: python3 -I make_cases.py <cases dir>

Each case directory gets input.txt (the raw model answer, written byte for
byte) and case.json (the Keys the case is loaded with, and where the input
comes from). Expected outputs are written separately by gen_goldens.py.

Inputs whose source starts with "upstream" are the inputs of PR-Agent's own
unit tests at commit 8e5a9295973b24af4b70cafd0b660a230811ef9e (MIT, see
NOTICE); the others are synthetic review answers written for review-mcp.
The mojibake inputs need PyYAML (yaml.safe_dump), as upstream's test does.
"""
import json
import os
import sys

import yaml

OUT = sys.argv[1]

# The keys upstream's try_fix_yaml always adds in tactic 1. review-mcp does
# not (DQ-8); cases taken from upstream tests that do not use review keys
# pass them explicitly so the Go chain sees the same key list.
BUILTIN = ["relevant line", "suggestion content", "relevant file", "existing code",
           "improved code", "label", "why", "suggestion_summary"]

# The v1 review keys (X-4) as test input. The descriptor table that derives
# the real list belongs to the review package (WP-PR-4c).
REVIEW = {
    "names": ["estimated_effort_to_review", "relevant_tests", "key_issues_to_review",
              "security_concerns", "relevant_file", "issue_header", "issue_content",
              "start_line", "end_line"],
    "first": "review",
    "last": "security_concerns",
}

# Like REVIEW, but without the list-valued key_issues_to_review (see the
# README: forcing that key into a block scalar swallows the whole list).
REVIEW_LEAF = dict(REVIEW, names=[n for n in REVIEW["names"] if n != "key_issues_to_review"])


def builtin(first="", last="", extra=()):
    return {"names": BUILTIN + list(extra), "first": first, "last": last}


CASES = []


def case(name, text, keys, source, canary=""):
    CASES.append((name, text, keys, source, canary))


def mojibake(obj):
    return yaml.safe_dump(obj, allow_unicode=True).encode("utf-8").decode("latin-1")


# --- tests/unittest/test_load_yaml.py -------------------------------------
src = "upstream tests/unittest/test_load_yaml.py"
case("up_load_valid", "name: John Smith\nage: 35", builtin(), src + "::test_load_valid_yaml")
case("up_load_invalid1", (
    "PR Analysis:\n"
    "  Main theme: Enhancing the `/describe` command prompt by adding title and description\n"
    "  Type of PR: Enhancement\n"
    "  Relevant tests: No\n"
    "  Focused PR: Yes, the PR is focused on enhancing the `/describe` command prompt.\n"
    "\n"
    "PR Feedback:\n"
    "  General suggestions: The PR seems to be well-structured and focused on a specific "
    "enhancement. However, it would be beneficial to add tests to ensure the new feature "
    "works as expected.\n"
    "  Code feedback:\n"
    "    - relevant file: pr_agent/settings/pr_description_prompts.toml\n"
    "      suggestion: Consider using a more descriptive variable name than 'user' for the "
    "command prompt. A more descriptive name would make the code more readable and "
    "maintainable. [medium]\n"
    '      relevant line: user="""PR Info: aaa\n'
    "  Security concerns: No"), builtin(), src + "::test_load_invalid_yaml1")
case("up_load_invalid2", (
    "- relevant file: src/app.py:\n"
    "  suggestion content: The print statement is outside inside the if __name__ ==: "),
    builtin(), src + "::test_load_invalid_yaml2")
case("up_control_char", "name: John\x08 Smith\nage: 35", builtin(),
     src + "::test_load_yaml_with_illegal_control_character")
case("up_control_char_broken", "relevant line: value\x08: 3\n", builtin(),
     src + "::test_load_yaml_with_illegal_control_character_and_broken_structure")
case("up_mojibake", mojibake({"suggestion content": "修复空指针异常"}), builtin(),
     src + "::test_load_yaml_does_not_strip_mojibake_repair_range")
case("up_sanitized_empty", "\x08\x08\x08", builtin(),
     src + "::test_load_yaml_sanitized_to_empty_does_not_return_none_silently")
case("up_empty", "", builtin(), src + "::test_load_yaml_genuinely_empty_input_unaffected")
case("up_fence_flush", "```yaml\nname: John\n```", builtin(), src + "::test_space_before_yaml_info_string")
case("up_fence_space_yaml", "``` yaml\nname: John\n```", builtin(), src + "::test_space_before_yaml_info_string")
case("up_fence_space_yml", "``` yml\nname: John\n```", builtin(), src + "::test_space_before_yaml_info_string")
for label in ["YAML", "YML", "Yaml", "yMl"]:
    case("up_fence_label_" + label, f"```\t{label}\t\nname: John\n```", builtin(),
         src + "::test_yaml_info_string_is_case_insensitive")
case("up_fence_text", "```text\nhello world\n```", builtin(),
     src + "::test_non_yaml_info_string_not_parsed_as_yaml_snippet")
case("up_fence_python", "```python\nname: John\n```", builtin(),
     src + "::test_non_yaml_info_string_not_parsed_as_yaml_snippet")
case("up_snippet_prefix", "prefix text\n```yaml\nname: John\n```", builtin(),
     src + "::test_snippet_fallback_with_surrounding_text")
case("up_snippet_prefix_space", "prefix text\n``` yaml\nname: John\n```", builtin(),
     src + "::test_snippet_fallback_with_surrounding_text")
case("up_yml_config", "yml_config:\n  a: 1", builtin(), src + "::test_key_starting_with_yml_is_not_truncated")
case("up_yml_true", "yml: true", builtin(), src + "::test_plain_yml_key_survives")
case("up_ymls_list", "ymls:\n  - a", builtin(), src + "::test_list_key_starting_with_yml_survives")

# --- tests/unittest/test_load_yaml_closing_fence.py -----------------------
src = "upstream tests/unittest/test_load_yaml_closing_fence.py"
ANSWER_ENDING_IN_A_FENCE = "response: |\n  Use this:\n  ```python\n  print(1)\n  ```\n"
case("up_cf_inner_fence", ANSWER_ENDING_IN_A_FENCE, builtin(),
     src + "::test_keep_a_closing_fence_that_belongs_to_the_content")
case("up_cf_wrapped", "```yaml\nresponse: |\n  hello\n```", builtin(),
     src + "::test_strip_the_wrapper_fence_the_model_was_primed_to_emit")
case("up_cf_wrapped_inner", "```yaml\n" + ANSWER_ENDING_IN_A_FENCE + "```", builtin(),
     src + "::test_strip_the_wrapper_fence_around_content_that_also_ends_in_one")
case("up_cf_primed_review", "review:\n  estimated_effort_to_review_[1-5]: |\n    2\n```", builtin(),
     src + "::test_strip_a_closing_fence_the_response_never_opened")
case("up_cf_primed_value", "response: |\n  hello\n```", builtin(),
     src + "::test_a_primed_answer_keeps_its_value_intact")
case("up_cf_unfenced", "response: |\n  hello\n", builtin(), src + "::test_an_unfenced_response_parses_unchanged")
case("up_cf_bare_yaml_prefix", "yaml\nresponse: |\n  hello\n", builtin(),
     src + "::test_a_bare_yaml_prefix_is_still_removed")

# --- tests/unittest/test_load_yaml_trailing_text.py -----------------------
src = "upstream tests/unittest/test_load_yaml_trailing_text.py"
SIGN_OFF = "\n\nI reviewed the diff and found nothing else worth flagging."
case("up_tt_wrapped_signoff", "```yaml\ncode_suggestions: []\n```" + SIGN_OFF,
     builtin("code_suggestions", "label"), src + "::test_a_wrapped_mapping_survives_a_sign_off")
case("up_tt_primed_signoff", "code_suggestions: []\n```" + SIGN_OFF,
     builtin("code_suggestions", "label"), src + "::test_a_primed_mapping_survives_a_sign_off")
case("up_tt_block_scalar_signoff", "```yaml\nresponse: |\n  hello\n```" + SIGN_OFF, builtin(),
     src + "::test_a_sign_off_is_not_folded_into_a_block_scalar")
case("up_tt_fenced_example", "```\nexample\n```\nresponse: hello", builtin(),
     src + "::test_an_answer_following_a_fenced_example_is_not_truncated")
case("up_tt_trailing_whitespace", "```yaml\nresponse: |\n  hello\n```  \n\n", builtin(),
     src + "::test_trailing_whitespace_after_the_fence_changes_nothing")
case("up_tt_payload_past_fence", "a: 1\n```\nb: 2", builtin(),
     src + "::test_payload_continuing_past_the_fence_still_fails_loudly")
case("up_tt_list_past_fence", "code_suggestions:\n  - one\n```\n  - two",
     builtin("code_suggestions", "label"), src + "::test_a_list_continuing_past_the_fence_still_fails_loudly")
case("up_tt_nested_past_fence", "review:\n  score: 1\n```\n  effort: 2", builtin(),
     src + "::test_a_nested_mapping_continuing_past_the_fence_still_fails_loudly")
case("up_tt_continuation_signoff",
     "code_suggestions:\n  - one\n```\nsecurity_concerns: 'No'\nscore: 8\n\nHope this helps.",
     builtin("code_suggestions", "security_concerns"),
     src + "::test_a_continuation_followed_by_a_sign_off_still_fails_loudly")
case("up_tt_crlf", "```yaml\r\nresponse: |\r\n  hello\r\n```\r\n\r\nThat is my answer.", builtin(),
     src + "::test_a_crlf_reply_is_repaired")
case("up_tt_signoff_mapping_like", "a: 1\n```\n\nNote: nothing else stood out.", builtin(),
     src + "::test_a_sign_off_that_reads_as_a_mapping_is_left_alone")
case("up_tt_second_block", "```yaml\nresponse: |\n  hello\n```\n\n```\nprint(1)\n```", builtin(),
     src + "::test_a_second_fenced_block_is_left_to_the_existing_behaviour")

# --- tests/unittest/test_load_yaml_unparseable.py -------------------------
src = "upstream tests/unittest/test_load_yaml_unparseable.py"
case("up_unparseable", "::: not : valid : yaml :::\n\t- [", builtin(),
     src + "::test_return_an_empty_mapping_for_an_unparseable_prediction")
case("up_parseable_review", "review:\n  score: 8", builtin(), src + "::test_a_parseable_prediction_is_unchanged")
for i, payload in enumerate(["code_suggestions:\n", "code_suggestions: 5\n", "code_suggestions:\n  a: 1\n"]):
    case(f"up_non_list_payload_{i + 1}", payload, builtin(),
         src + "::test_a_non_list_code_suggestions_value_is_rejected")

# --- tests/unittest/test_output_models.py ---------------------------------
src = "upstream tests/unittest/test_output_models.py"
case("up_effort_int", "estimated_effort_to_review_[1-5]: 3", builtin(),
     src + "::test_estimate_effort_example_is_a_strict_integer")
case("up_ranking", 'which_response_was_better: 1\nwhy: "It is clearer."\nscore_response1: 9\nscore_response2: 7',
     builtin(), src + "::test_ranking_example_is_a_valid_numeric_payload")

# --- tests/unittest/test_try_fix_yaml.py ----------------------------------
src = "upstream tests/unittest/test_try_fix_yaml.py"
case("up_tf_valid", "key: value\n", builtin(), src + "::test_valid_yaml")
case("up_tf_relevant_line", "relevant line: value: 3\n", builtin(), src + "::test_add_relevant_line")
for i, label in enumerate(["yaml", "yml", ""]):
    case(f"up_tf_snippet_{i + 1}",
         f"Here is the answer in YAML format:\n\n```{label}\nname: John Smith\nage: 35\n```\n",
         builtin(), src + "::test_extract_snippet")
GARBAGE = "{\nSome garbage: [unclosed\n\n"
case("up_tf_first_key",
     GARBAGE + "analysis:\n  details:\n    summary: example\nissue:\n  id: 1\n\nTrailing: ???\n",
     builtin("analysis", "issue"), src + "::test_first_key_extraction_preserves_key_name")
for value in ["general", "security", "maintainability"]:
    case("up_tf_last_value_" + value, GARBAGE + f"review:\n  summary: example\nlabel: {value}\n",
         builtin("review", "label"), src + "::test_last_value_extraction_preserves_scalar")
for n in [1, 2, 3, 4, 6]:
    case(f"up_tf_backticks_{n}", GARBAGE + "review:\n  summary: example\nissue:\n  id: 1\n" + "`" * n + "\n",
         builtin("review", "issue"), src + "::test_key_extraction_accepts_trailing_backtick_runs")
for closing in ["```yaml", "```yml", "```YAML", "```YML"]:
    case("up_tf_closing_" + closing.strip("`"),
         GARBAGE + "review:\n  summary: example\nissue:\n  id: 1\n" + closing + "\n",
         builtin("review", "issue"), src + "::test_key_extraction_accepts_labeled_closing_fence")
SUGGESTIONS = (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    src/index.ts\n"
    "  label: |\n"
    "    best practice\n"
    "\n"
    "- relevant_file: |\n"
    "    src/index2.ts\n"
    "  label: |\n"
    "    enhancement\n")
case("up_tf_no_initial_yaml",
     "I suggest the following:\n\n" + SUGGESTIONS + "```\n\nWe can further improve the code by using the "
     "`const` keyword instead of `var` in the `src/index.ts` file.\n",
     builtin("code_suggestions", "label"), src + "::test_no_initial_yaml")
case("up_tf_with_initial_yaml",
     "I suggest the following:\n\n```\n" + SUGGESTIONS + "```\n\nWe can further improve the code by using the "
     "`const` keyword instead of `var` in the `src/index.ts` file.\n",
     builtin("code_suggestions", "label"), src + "::test_with_initial_yaml")
case("up_tf_brackets", "{\n" + SUGGESTIONS + "}\n", builtin("code_suggestions", "label"),
     src + "::test_with_brackets_yaml_content")
case("up_tf_tab_indent", SUGGESTIONS.replace("    best practice", "\tbest practice"),
     builtin("code_suggestions", "label"), src + "::test_tab_indent_yaml")
case("up_tf_leading_plus", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    src/index.ts\n"
    "  label: |\n"
    "    best practice\n"
    "  existing_code: |\n"
    "+   var router = createBrowserRouter([\n"
    "  improved_code: |\n"
    "+   const router = createBrowserRouter([\n"),
    builtin("code_suggestions", "improved_code"), src + "::test_leading_plus_mark_code")
case("up_tf_brace_inconsistent", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    tsconfig.json\n"
    "  existing_code: |\n"
    "     {\n"
    "        \"key1\": \"value1\",\n"
    "        \"key2\": {\n"
    "          \"subkey\": \"value\"\n"
    "         }\n"
    "    }\n"),
    builtin("code_suggestions", "existing_code"), src + "::test_inconsistent_indentation_in_block_scalar_yaml")
case("up_tf_brace_insufficient", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    tsconfig.json\n"
    "  existing_code: |\n"
    "    {\n"
    "      \"key1\": \"value1\",\n"
    "      \"key2\": {\n"
    "        \"subkey\": \"value\"\n"
    "      }\n"
    "  }\n"),
    builtin("code_suggestions", "existing_code"),
    src + "::test_inconsistent_and_insufficient_indentation_in_block_scalar_yaml")
case("up_tf_stray_brace", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    example.py\n"
    "  improved_code: |\n"
    "    return shared_helper(x)\n"
    "  }\n"),
    builtin("code_suggestions", "improved_code"), src + "::test_stray_closing_brace_is_not_added_to_block_scalar")
case("up_tf_stray_brace_before_key", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    example.py\n"
    "  improved_code: |\n"
    "    return shared_helper(x)\n"
    "  }\n"
    "  label: |\n"
    "    maintainability\n"),
    builtin("code_suggestions", "improved_code"),
    src + "::test_stray_closing_brace_before_following_key_is_not_added_to_block_scalar")
case("up_tf_brace_in_string", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    example.js\n"
    "  existing_code: |\n"
    "    if (x) {\n"
    "      console.log(\"}\");\n"
    "  }\n"),
    builtin("code_suggestions", "existing_code"),
    src + "::test_closing_brace_after_brace_in_string_stays_in_block_scalar")
case("up_tf_brace_after_else", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    example.js\n"
    "  existing_code: |\n"
    "    } else {\n"
    "      fallback()\n"
    "  }\n"),
    builtin("code_suggestions", "existing_code"),
    src + "::test_closing_brace_after_else_block_stays_in_block_scalar")
case("up_tf_wrong_indentation", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    a.c\n"
    "  existing_code: |\n"
    "  int sum(int a, int b) {\n"
    "    return a + b;\n"
    "  }\n"
    "\n"
    "  int sub(int a, int b) {\n"
    "    return a - b;\n"
    "  }\n"),
    builtin("code_suggestions", "existing_code"), src + "::test_wrong_indentation_code_block_scalar")
case("up_tf_diff_markers", (
    "code_suggestions:\n"
    "- relevant_file: |\n"
    "    example.rb\n"
    "  existing_code: |\n"
    "+    puts 'hello'\n"
    "+    puts 'world'\n"
    "- relevant_file: |\n"
    "-    example.py\n"
    "-  existing_code: |\n"
    "-+    print('hello')\n"
    "-+    print('world')\n"),
    builtin("code_suggestions", "existing_code"), src + "::test_diff_markers_removed_within_list_item")
case("up_tf_diff_marker_none", "-\n-#x", builtin(),
     src + "::test_diff_marker_fallback_does_not_log_success_on_none")

# --- synthetic review answers (review-mcp) --------------------------------
src = "synthetic"
WELL_FORMED = (
    "review:\n"
    "  estimated_effort_to_review: 3\n"
    "  relevant_tests: No\n"
    "  key_issues_to_review:\n"
    "    - relevant_file: |\n"
    "        internal/app/server.go\n"
    "      issue_header: |\n"
    "        Possible nil dereference\n"
    "      issue_content: |\n"
    "        The handler reads cfg.Name before checking cfg: a nil config panics.\n"
    "      start_line: 41\n"
    "      end_line: 44\n"
    "  security_concerns: |\n"
    "    No\n")
case("rv_direct", WELL_FORMED, REVIEW, src)
case("rv_direct_fenced", "```yaml\n" + WELL_FORMED + "```", REVIEW, src)
case("rv_direct_primed_signoff", WELL_FORMED + "```\n\nLet me know if anything is unclear.", REVIEW, src)
case("rv_duplicate_keys",
     "review:\n  estimated_effort_to_review: 2\n  estimated_effort_to_review: 4\n  security_concerns: No\n",
     REVIEW, src)
case("rv_yaml11_words",
     "review:\n  relevant_tests: Yes\n  security_concerns: No\n  issue_header: on\n  start_line: 0755\n"
     "  end_line: 1e3\n  issue_content: 2024-01-01\n", REVIEW, src)

# Canaries (spec P4 §3): each answer is repaired by exactly one tactic: the
# direct parse and every earlier tactic fail, and this tactic succeeds.
case("c01_block_scalar_keys", (
    "review:\n"
    "  estimated_effort_to_review: 2\n"
    "  relevant_tests: No\n"
    "  security_concerns: Possible token leak: the API key is written to the debug log.\n"),
    REVIEW, src, canary="block_scalar_keys")
case("c02_explicit_indent", (
    "review:\n"
    "  key_issues_to_review:\n"
    "    - relevant_file: |\n"
    "        internal/app/server.go\n"
    "      issue_header: |\n"
    "        Unchecked error\n"
    "      issue_content: |\n"
    "            if err := f(); err != nil {\n"
    "        return\n"
    "            }\n"
    "      start_line: 12\n"
    "      end_line: 14\n"
    "  security_concerns: |\n"
    "    No\n"),
    REVIEW, src, canary="explicit_indent")
case("c04_fenced_snippet", "Here is my review of the change:\n```yaml\n" + WELL_FORMED + "```",
     REVIEW, src, canary="fenced_snippet")
case("c05_strip_braces", (
    "{\n"
    "review:\n"
    "  estimated_effort_to_review: 1\n"
    "  relevant_tests: Yes\n"
    "  key_issues_to_review: []\n"
    "  security_concerns: |\n"
    "    No\n"
    "}"),
    REVIEW, src, canary="strip_braces")
case("c06_key_window", (
    "Sure. Here is the review of the change, as requested.\n"
    "\n" + WELL_FORMED +
    "\n"
    "I hope this helps, and let me know if you need anything else.\n"),
    REVIEW, src, canary="key_window")
case("c07_strip_plus", (
    "review:\n"
    "  key_issues_to_review:\n"
    "    - relevant_file: |\n"
    "        internal/app/server.go\n"
    "      issue_header: |\n"
    "        Ignored error\n"
    "      issue_content: |\n"
    "        The new call drops its error:\n"
    "+       f()\n"
    "      start_line: 20\n"
    "      end_line: 20\n"
    "  security_concerns: |\n"
    "    No\n"),
    REVIEW, src, canary="strip_plus")
case("c08_diff_markers", (
    "review:\n"
    "  key_issues_to_review:\n"
    "    - relevant_file: |\n"
    "        internal/app/server.go\n"
    "      issue_header: |\n"
    "        Removed check\n"
    "      issue_content: |\n"
    "        The error check was removed:\n"
    "-        if err != nil {\n"
    "-          return err\n"
    "-        }\n"
    "      start_line: 30\n"
    "      end_line: 32\n"
    "  security_concerns: |\n"
    "    No\n"),
    REVIEW, src, canary="diff_markers")
case("c09_tabs_to_spaces", (
    "review:\n"
    "  estimated_effort_to_review: 2\n"
    "  security_concerns: |\n"
    "\tNo\n"),
    REVIEW, src, canary="tabs_to_spaces")
# The blank line inside the quoted value ends tactic 6's window early, so
# that tactic cannot rescue the answer by cutting the stray pipe off.
case("c11_root_pipe", (
    "|\n"
    "review:\n"
    "  estimated_effort_to_review: 2\n"
    "  relevant_tests: No\n"
    "  security_concerns: \"Possible token leak.\n"
    "\n"
    "    The API key is written to the debug log.\"\n"),
    REVIEW, src, canary="root_pipe")
case("c12_reencode", mojibake({"review": {
    "estimated_effort_to_review": 2,
    "security_concerns": "Güvenlik açığı: kullanıcı girdisi doğrulanmıyor.",
}}), REVIEW, src, canary="reencode")

# Review key lists with and without key_issues_to_review in tactic 1 (see
# the README, "Review keys and tactic 1").
LIST_COLON = (
    "review:\n"
    "  estimated_effort_to_review: 2\n"
    "  key_issues_to_review:\n"
    "    - relevant_file: internal/app/server.go\n"
    "      issue_header: Nil dereference\n"
    "      issue_content: The handler reads cfg.Name: a nil config panics.\n"
    "      start_line: 41\n"
    "      end_line: 44\n"
    "  security_concerns: No\n")
case("rv_list_colon_all_keys", LIST_COLON, REVIEW, src)
case("rv_list_colon_leaf_keys", LIST_COLON, REVIEW_LEAF, src)


def main():
    os.makedirs(OUT, exist_ok=True)
    names = set()
    for name, text, keys, source, canary in CASES:
        assert name not in names, name
        names.add(name)
        d = os.path.join(OUT, name)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "input.txt"), "w", encoding="utf-8", newline="") as fh:
            fh.write(text)
        meta = {"names": keys["names"], "first": keys["first"], "last": keys["last"], "source": source}
        if canary:
            meta["canary"] = canary
        with open(os.path.join(d, "case.json"), "w", encoding="utf-8") as fh:
            json.dump(meta, fh, indent=2, ensure_ascii=True)
            fh.write("\n")


main()
