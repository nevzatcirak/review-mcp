package data

import "sort"

// FrameworkNames returns the sorted names of the generated-code frameworks.
func FrameworkNames() []string {
	names := make([]string, 0, len(GeneratedCode))
	for n := range GeneratedCode {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
