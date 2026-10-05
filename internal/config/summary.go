package config

import (
	"reflect"

	"github.com/nevzatcirak/review-mcp/internal/logging"
)

// Summary is a JSON-serializable, secret-free view of the effective
// configuration.
type Summary struct {
	// Values maps every non-secret §5 key to its effective value and source.
	// Unset optional values are null. URL values are passed through
	// logging.RedactURL.
	Values map[string]SummaryValue `json:"values"`
	// Secrets maps each secret key to "set" or "unset".
	Secrets map[string]string `json:"secrets"`
	// Providers lists the enabled providers.
	Providers []ProviderSummary `json:"providers"`
	// Warnings are the load warnings.
	Warnings []string `json:"warnings"`
}

// SummaryValue is one effective value and the layer that supplied it.
type SummaryValue struct {
	Value  any    `json:"value"`
	Source Origin `json:"source"`
}

// ProviderSummary describes an enabled provider.
type ProviderSummary struct {
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
}

// Summary builds the secret-free view. r may be nil (all sources then read
// "default").
func (c *Config) Summary(r *Report) Summary {
	s := Summary{
		Values:    make(map[string]SummaryValue, len(table)),
		Secrets:   make(map[string]string, len(secretTable)),
		Providers: []ProviderSummary{},
		Warnings:  []string{},
	}
	for _, e := range table {
		src := OriginDefault
		if r != nil {
			if o, ok := r.Sources[e.key]; ok {
				src = o
			}
		}
		s.Values[e.key] = SummaryValue{Value: summaryValue(e, c.valueOf(e)), Source: src}
	}
	for _, se := range secretTable {
		if se.ptr(c).IsSet() {
			s.Secrets[se.key] = "set"
		} else {
			s.Secrets[se.key] = "unset"
		}
	}
	if c.Gitea.BaseURL != "" {
		s.Providers = append(s.Providers, ProviderSummary{Kind: "gitea", BaseURL: logging.RedactURL(c.Gitea.BaseURL)})
	}
	if c.BitbucketServer.BaseURL != "" {
		s.Providers = append(s.Providers, ProviderSummary{Kind: "bitbucket_server", BaseURL: logging.RedactURL(c.BitbucketServer.BaseURL)})
	}
	if r != nil {
		s.Warnings = append(s.Warnings, r.Warnings...)
	}
	return s
}

// valueOf dereferences the field behind e, mapping nil optionals to nil.
func (c *Config) valueOf(e entry) any {
	v := reflect.ValueOf(e.ptr(c)).Elem()
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	return v.Interface()
}

func summaryValue(e entry, v any) any {
	switch x := v.(type) {
	case string:
		if urlKeys[e.key] && x != "" {
			return logging.RedactURL(x)
		}
	case []string:
		return append([]string{}, x...)
	}
	return v
}
