package config

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// leaf describes one non-secret Config field discovered by reflection.
type leaf struct {
	key  string
	addr uintptr
	typ  reflect.Type
}

func structLeaves(t *testing.T, c *Config) map[string]leaf {
	t.Helper()
	out := map[string]leaf{}
	rv := reflect.ValueOf(c).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Name == "Secrets" {
			continue
		}
		sec := f.Tag.Get("toml")
		if sec == "" || sec == "-" {
			t.Fatalf("Config.%s has no toml tag", f.Name)
		}
		sv := rv.Field(i)
		for j := 0; j < sv.NumField(); j++ {
			ff := sv.Type().Field(j)
			tk := ff.Tag.Get("toml")
			if tk == "" {
				t.Fatalf("%s.%s has no toml tag", f.Name, ff.Name)
			}
			if jk := ff.Tag.Get("json"); jk != tk {
				t.Errorf("%s.%s: json tag %q differs from toml tag %q", f.Name, ff.Name, jk, tk)
			}
			out[sec+"."+tk] = leaf{key: sec + "." + tk, addr: sv.Field(j).Addr().Pointer(), typ: ff.Type}
		}
	}
	return out
}

// TestTableMatchesStruct fails when a Config field has no table row or a table
// row has no Config field, so neither can be added without the other. [canary]
func TestTableMatchesStruct(t *testing.T) {
	c := Defaults()
	leaves := structLeaves(t, c)

	seenKey, seenEnv := map[string]bool{}, map[string]bool{}
	for _, e := range table {
		if seenKey[e.key] {
			t.Errorf("duplicate table key %s", e.key)
		}
		if seenEnv[e.env] {
			t.Errorf("duplicate table env %s", e.env)
		}
		seenKey[e.key], seenEnv[e.env] = true, true

		want := EnvPrefix + strings.ToUpper(strings.ReplaceAll(e.key, ".", "_"))
		if e.env != want {
			t.Errorf("key %s: env %s does not follow the naming convention (want %s)", e.key, e.env, want)
		}
		lf, ok := leaves[e.key]
		if !ok {
			t.Errorf("table row %s has no Config field", e.key)
			continue
		}
		pv := reflect.ValueOf(e.ptr(c))
		if pv.Pointer() != lf.addr {
			t.Errorf("table row %s points at the wrong Config field", e.key)
		}
		if pv.Type().Elem() != lf.typ {
			t.Errorf("table row %s: pointer type %v, field type %v", e.key, pv.Type().Elem(), lf.typ)
		}
	}
	for key := range leaves {
		if !seenKey[key] {
			t.Errorf("Config field %s has no table row (no env variable)", key)
		}
	}

	// Secrets: one table row per Secrets field, none leaking into the file table.
	st := reflect.TypeOf(Secrets{})
	if st.NumField() != len(secretTable) {
		t.Errorf("Secrets has %d fields, secretTable has %d rows", st.NumField(), len(secretTable))
	}
	for i := 0; i < st.NumField(); i++ {
		if st.Field(i).Type != reflect.TypeOf(Secret{}) {
			t.Errorf("Secrets.%s is not a Secret", st.Field(i).Name)
		}
	}
	for _, s := range secretTable {
		if _, ok := byKey[s.key]; ok {
			t.Errorf("secret %s must not be a file key", s.key)
		}
	}
}

// sampleFor returns an env string that differs from the current default.
func sampleFor(p any) string {
	switch v := p.(type) {
	case *string:
		return "sample"
	case **string:
		return "sample"
	case *int, **int:
		return "7"
	case **int64:
		return "-9"
	case *float64, **float64:
		return "0.75"
	case *bool:
		return strconv.FormatBool(!*v)
	case *[]string:
		return "a, b ,c"
	}
	panic("unsupported type")
}

// TestEveryEnvVariableParses sets each table variable on its own and checks the
// right field changes and the source becomes env. [canary]
func TestEveryEnvVariableParses(t *testing.T) {
	for _, e := range table {
		t.Run(e.env, func(t *testing.T) {
			cfg := Defaults()
			sample := sampleFor(e.ptr(cfg))
			l := &loader{src: MemSource{Env: map[string]string{e.env: sample}}, cfg: cfg, rep: newReport(), bad: map[string]bool{}}
			l.loadEnv()
			if len(l.problems) != 0 {
				t.Fatalf("problems: %v", l.problems)
			}
			if l.rep.Sources[e.key] != OriginEnv {
				t.Errorf("source = %q, want env", l.rep.Sources[e.key])
			}
			if reflect.DeepEqual(e.ptr(cfg), e.ptr(Defaults())) {
				t.Errorf("field for %s did not change", e.key)
			}
			// Every other table field is untouched.
			def := Defaults()
			for _, o := range table {
				if o.key == e.key {
					continue
				}
				if !reflect.DeepEqual(o.ptr(cfg), o.ptr(def)) {
					t.Errorf("setting %s also changed %s", e.env, o.key)
				}
			}
		})
	}
}

func TestSecretEnvVariablesLoad(t *testing.T) {
	cfg, rep := mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_BITBUCKET_SERVER_TOKEN": fakeBitbkt, "REVIEW_MCP_BITBUCKET_SERVER_BASE_URL": "https://bitbucket.example.com/stash", "REVIEW_MCP_SERVE_ACCESS_TOKEN": fakeAccess})})
	for _, s := range secretTable {
		if !s.ptr(cfg).IsSet() {
			t.Errorf("%s not loaded", s.env)
		}
		if rep.Sources[s.key] != OriginEnv {
			t.Errorf("source of %s = %q", s.key, rep.Sources[s.key])
		}
	}
	if cfg.Secrets.LLMAPIKey.Reveal() != fakeLLMKey {
		t.Error("Reveal did not return the stored value")
	}
}
