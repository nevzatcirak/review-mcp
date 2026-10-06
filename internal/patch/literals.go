package patch

// Contract strings reproduced verbatim from PR-Agent
// (https://github.com/The-PR-Agent/pr-agent) at commit
// 8e5a9295973b24af4b70cafd0b660a230811ef9e (tag v0.47.0), MIT License.
// See NOTICE for the copyright and permission notice (X-7, spec §0.2).
//
// The prompts (P4) anchor on these strings, so they must not change. Sources:
//   - pr_agent/algo/git_patch_processing.py: RE_HUNK_HEADER,
//     NO_NEWLINE_AT_EOF_MARKER, process_patch_lines (rewritten hunk header),
//     decouple_and_convert_to_hunks_with_lines_numbers (file header,
//     deleted-file line, __new hunk__ / __old hunk__ markers);
//   - pr_agent/algo/pr_processing.py: pr_generate_extended_diff and
//     generate_full_patch (plain "## File:" header), _unreadable_file_notice
//     (unreadable-file notice), and the DELETED_FILES_, MORE_MODIFIED_FILES_
//     and ADDED_FILES_ constants (omitted-file section headers).

// hunkHeaderPattern is upstream's RE_HUNK_HEADER. The capture groups are the
// old start, old count, new start, new count and the section text.
//
// Python's \d also matches non-ASCII decimal digits; Go's matches ASCII
// only, so a header written with non-ASCII digits is treated as malformed
// here. Providers never emit such headers.
const hunkHeaderPattern = `^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@[ ]?(.*)`

// noNewlineMarker is upstream's NO_NEWLINE_AT_EOF_MARKER.
const noNewlineMarker = `\ No newline at end of file`

// extendedHunkHeaderFormat is the hunk header process_patch_lines writes for
// every hunk it processes. The space before the section text is written even
// when the section is empty.
const extendedHunkHeaderFormat = "@@ -%d,%d +%d,%d @@ %s"

// File headers. The plain header is followed by a blank line; the decoupled
// header is not.
const (
	plainFileHeaderPrefix     = "\n\n## File: '"
	plainFileHeaderSuffix     = "'\n\n"
	decoupledFileHeaderPrefix = "\n\n## File: '"
	decoupledFileHeaderSuffix = "'\n"
	deletedFilePrefix         = "\n\n## File '"
	deletedFileSuffix         = "' was deleted\n"
)

// Decoupled hunk block markers.
const (
	newHunkMarker = "__new hunk__"
	oldHunkMarker = "__old hunk__"
)

// unreadableNoticeBody is the body of upstream's _unreadable_file_notice,
// written after the plain file header.
const unreadableNoticeBody = "> **This file could not be read.** PR-Agent failed to fetch its contents, so no diff " +
	"is available and nothing in it was reviewed. Do not assume this file is correct, " +
	"unchanged, or free of issues: flag it for manual review.\n"

// Omitted-file section headers, for the assembly step (WP-PR-3d). Each is
// followed by the file names, one per line.
const (
	// AddedFilesHeader is upstream's ADDED_FILES_.
	AddedFilesHeader = "Additional added files (insufficient token budget to process):\n"
	// ModifiedFilesHeader is upstream's MORE_MODIFIED_FILES_.
	ModifiedFilesHeader = "Additional modified files (insufficient token budget to process):\n"
	// DeletedFilesHeader is upstream's DELETED_FILES_.
	DeletedFilesHeader = "Deleted files:\n"
)
