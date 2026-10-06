package prompt

import "strings"

// The output-language instruction is adapted from PR-Agent
// (https://github.com/The-PR-Agent/pr-agent) at commit
// 8e5a9295973b24af4b70cafd0b660a230811ef9e, pr_agent/agent/pr_agent.py
// (PRAgent._handle_request: lang_instruction_text and separator_text), MIT
// License. See NOTICE for the copyright and permission notice (X-7). The
// sentence and the separator are verbatim; only the locale code varies.

// DefaultLanguage is the output language that needs no instruction.
const DefaultLanguage = "en-US"

const (
	languageInstructionPrefix = "Your response MUST be written in the language corresponding to locale code: '"
	languageInstructionSuffix = "'. This is crucial. Keep schema control values (such as 'No', 'Yes', " +
		"'None', 'false') in their original English form and do not translate them."
	// languageSeparator joins the instruction to non-empty extra
	// instructions.
	languageSeparator = "\n======\n\nIn addition, "
)

// LanguageInstruction is upstream's fixed English sentence asking for an
// answer in the given locale. It keeps schema control values such as "No"
// in English, which the No-detector relies on.
func LanguageInstruction(language string) string {
	return languageInstructionPrefix + language + languageInstructionSuffix
}

// WithOutputLanguage returns the extra instructions for an effective output
// language, upstream's way (spec P4 §4.2): when language is not en-US
// (compared case-insensitively, as upstream does) the language instruction
// is appended to extra with upstream's separator, or becomes the whole text
// when extra is empty. Like upstream, nothing is appended when extra
// already contains the instruction. An empty language means en-US. Keys
// and the schema are never translated.
func WithOutputLanguage(extra, language string) string {
	if language == "" || strings.EqualFold(language, DefaultLanguage) {
		return extra
	}
	instr := LanguageInstruction(language)
	switch {
	case strings.Contains(extra, instr):
		return extra
	case extra == "":
		return instr
	}
	return extra + languageSeparator + instr
}
