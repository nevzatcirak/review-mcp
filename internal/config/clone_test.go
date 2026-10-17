package config

import (
	"reflect"
	"testing"
)

// TestCloneSharesNothing fills every pointer and slice of a Config, clones
// it, and checks that no pointer and no slice of the clone shares memory with
// the original. A field added later with a slice or a pointer fails here
// until Clone copies it. [canary]
func TestCloneSharesNothing(t *testing.T) {
	c := Defaults()
	fill(reflect.ValueOf(c).Elem())
	cp := c.Clone()
	if !reflect.DeepEqual(c, cp) {
		t.Fatalf("the clone differs from the original:\n got %+v\nwant %+v", cp, c)
	}
	orig := map[uintptr]string{}
	collect(reflect.ValueOf(c).Elem(), "Config", func(p uintptr, path string) { orig[p] = path })
	collect(reflect.ValueOf(cp).Elem(), "Config", func(p uintptr, path string) {
		if o, ok := orig[p]; ok {
			t.Errorf("%s shares memory with the original's %s", path, o)
		}
	})
	if len(orig) < 10 {
		t.Errorf("only %d pointers and slices were found; the walk is broken", len(orig))
	}
}

// fill sets every nil pointer and every slice to a non-empty value.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fill(v.Elem())
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := 0; i < 2; i++ {
			fill(s.Index(i))
		}
		v.Set(s)
	}
}

// collect reports the address of every pointer target and slice backing
// array below v.
func collect(v reflect.Value, path string, seen func(uintptr, string)) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				collect(v.Field(i), path+"."+v.Type().Field(i).Name, seen)
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			seen(v.Pointer(), path)
			collect(v.Elem(), path, seen)
		}
	case reflect.Slice:
		if v.Len() > 0 {
			seen(v.Pointer(), path)
			for i := 0; i < v.Len(); i++ {
				collect(v.Index(i), path, seen)
			}
		}
	}
}
