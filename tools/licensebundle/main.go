// Command licensebundle generates THIRD_PARTY_LICENSES, the bundle of license
// and notice texts of every third-party Go module linked into the review-mcp
// binary. It is a build-time tool: it is not shipped and not imported by
// cmd/review-mcp. It uses the standard library only.
//
// Usage (from the repository root):
//
//	go run ./tools/licensebundle [-out THIRD_PARTY_LICENSES] [-allowlist tools/licensebundle/allowlist.txt] [-pkg ./cmd/review-mcp]
//
// The generator fails when a linked module has no allowlist entry, has no
// license file, or has an SPDX id outside the permitted set, and when the
// allowlist names a module that is not linked (a stale entry).
//
// The linked set is computed for the host platform. The release targets
// (linux, darwin, windows on amd64 and arm64) link the same module set at the
// time of writing; a platform-specific dependency would need a per-platform
// union here.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// permittedSPDX is the set of license ids that may be bundled without an
// architect decision.
var permittedSPDX = map[string]bool{
	"MIT":          true,
	"BSD-2-Clause": true,
	"BSD-3-Clause": true,
	"Apache-2.0":   true,
	"ISC":          true,
}

// goListTemplate prints "path<TAB>version<TAB>dir" for every dependency that
// belongs to a module other than the main module. A replace directive is
// honored: the replacement's version and directory are reported, the original
// module path stays the identity.
const goListTemplate = `{{with .Module}}{{if not .Main}}{{.Path}}{{"\t"}}{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}{{"\t"}}{{.Dir}}{{end}}{{end}}`

// module is one linked third-party module.
type module struct {
	Path    string
	Version string
	Dir     string
}

// licenseFile is one license or notice file of a module.
type licenseFile struct {
	Name string
	Text string
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "licensebundle:", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("licensebundle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "THIRD_PARTY_LICENSES", "output file")
	allowPath := fs.String("allowlist", "tools/licensebundle/allowlist.txt", "allowlist file")
	pkg := fs.String("pkg", "./cmd/review-mcp", "main package whose linked modules are bundled")
	if err := fs.Parse(args); err != nil {
		return err
	}

	f, err := os.Open(*allowPath)
	if err != nil {
		return fmt.Errorf("open allowlist: %w", err)
	}
	allow, err := parseAllowlist(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("allowlist %s: %w", *allowPath, err)
	}

	mods, err := listModules(*pkg)
	if err != nil {
		return err
	}

	doc, err := build(mods, allow)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, []byte(doc), 0o600) // file mode is irrelevant to the repository content
}

// listModules returns the modules linked into pkg for the host platform,
// deduplicated and sorted by path.
func listModules(pkg string) ([]module, error) {
	cmd := exec.Command("go", "list", "-deps", "-f", goListTemplate, pkg) //nolint:gosec // build-time tool; pkg is a developer-supplied flag
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseGoList(stdout.String())
}

// parseGoList parses the output of goListTemplate.
func parseGoList(out string) ([]module, error) {
	seen := map[string]module{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected go list line %q", line)
		}
		m := module{Path: parts[0], Version: parts[1], Dir: parts[2]}
		if m.Path == "" {
			return nil, fmt.Errorf("go list line without module path: %q", line)
		}
		if prev, ok := seen[m.Path]; ok && prev != m {
			return nil, fmt.Errorf("module %s listed with conflicting data", m.Path)
		}
		seen[m.Path] = m
	}
	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

// parseAllowlist parses lines of the form "module SPDX-expression". Blank
// lines and lines starting with '#' are ignored. The expression is everything
// after the module path, for example "MIT" or "MIT AND Apache-2.0".
func parseAllowlist(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	allow := map[string]string{}
	var errs []error
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			errs = append(errs, fmt.Errorf("line %d: want \"module SPDX\"", i+1))
			continue
		}
		path, expr := fields[0], strings.Join(fields[1:], " ")
		if _, dup := allow[path]; dup {
			errs = append(errs, fmt.Errorf("line %d: duplicate entry for %s", i+1, path))
			continue
		}
		allow[path] = expr
	}
	return allow, errors.Join(errs...)
}

// checkSPDX accepts a single permitted id or several permitted ids joined by
// " AND " (a module whose files are covered by more than one license).
func checkSPDX(expr string) error {
	for _, id := range strings.Split(expr, " AND ") {
		if !permittedSPDX[id] {
			return fmt.Errorf("SPDX id %q is outside the permitted set (MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC); needs an architect decision", id)
		}
	}
	return nil
}

// licenseFiles reads the LICENSE*, LICENCE*, COPYING* and NOTICE* files in dir
// (not recursively), sorted by name. hasLicense reports whether at least one
// of them is a license file rather than a NOTICE.
func licenseFiles(dir string) (files []licenseFile, hasLicense bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		up := strings.ToUpper(e.Name())
		isLicense := strings.HasPrefix(up, "LICENSE") || strings.HasPrefix(up, "LICENCE") || strings.HasPrefix(up, "COPYING")
		isNotice := strings.HasPrefix(up, "NOTICE")
		if !isLicense && !isNotice {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // dir comes from go list; the name is a directory entry
		if err != nil {
			return nil, false, err
		}
		files = append(files, licenseFile{Name: e.Name(), Text: normalize(string(b))})
		if isLicense {
			hasLicense = true
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, hasLicense, nil
}

// normalize converts line endings to LF and trims trailing whitespace at the
// end of the text so that the bundle does not depend on the platform.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, " \t\r\n")
}

// build validates mods against allow and renders the bundle. All violations
// are reported together.
func build(mods []module, allow map[string]string) (string, error) {
	type entry struct {
		mod   module
		spdx  string
		files []licenseFile
	}
	var (
		errs    []error
		entries []entry
	)
	linked := map[string]bool{}

	sorted := append([]module(nil), mods...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	for _, m := range sorted {
		linked[m.Path] = true
		spdx, ok := allow[m.Path]
		if !ok {
			errs = append(errs, fmt.Errorf("module %s %s is linked but has no allowlist entry", m.Path, m.Version))
			continue
		}
		if err := checkSPDX(spdx); err != nil {
			errs = append(errs, fmt.Errorf("module %s: %w", m.Path, err))
			continue
		}
		if m.Dir == "" {
			errs = append(errs, fmt.Errorf("module %s has no source directory (not downloaded?)", m.Path))
			continue
		}
		files, hasLicense, err := licenseFiles(m.Dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("module %s: read license files: %w", m.Path, err))
			continue
		}
		if !hasLicense {
			errs = append(errs, fmt.Errorf("module %s has no LICENSE, LICENCE or COPYING file", m.Path))
			continue
		}
		entries = append(entries, entry{mod: m, spdx: spdx, files: files})
	}
	var stale []string
	for p := range allow {
		if !linked[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	for _, p := range stale {
		errs = append(errs, fmt.Errorf("allowlist entry %s is not linked into the binary (stale entry)", p))
	}
	if len(errs) > 0 {
		return "", errors.Join(errs...)
	}

	const rule = "================================================================================\n"
	var b strings.Builder
	b.WriteString("THIRD-PARTY LICENSES\n\n")
	b.WriteString("review-mcp is distributed with the Go modules listed below, linked into the\n")
	b.WriteString("binary. Each section gives the module path, version, SPDX license id and the\n")
	b.WriteString("full license and notice texts shipped by the module. This file is generated by\n")
	b.WriteString("tools/licensebundle; do not edit it by hand.\n\n")
	fmt.Fprintf(&b, "Modules (%d):\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(&b, "  %s %s (%s)\n", e.mod.Path, e.mod.Version, e.spdx)
	}
	for _, e := range entries {
		b.WriteString("\n" + rule)
		fmt.Fprintf(&b, "Module:  %s\nVersion: %s\nLicense: %s\n", e.mod.Path, e.mod.Version, e.spdx)
		for _, f := range e.files {
			b.WriteString("\n--- " + f.Name + " ---\n\n")
			b.WriteString(f.Text + "\n")
		}
	}
	return b.String(), nil
}
