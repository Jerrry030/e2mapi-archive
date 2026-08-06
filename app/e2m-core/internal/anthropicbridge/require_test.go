package anthropicbridge

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// require reimplements the small slice of testify the ported sub2api spec
// suite uses, so E2M keeps its stdlib-only test convention.
type requireT struct{}

var require requireT

func (requireT) NoError(t *testing.T, err error, args ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v %v", err, args)
	}
}

func (requireT) Equal(t *testing.T, want, got any, args ...any) {
	t.Helper()
	if !equalValues(want, got) {
		t.Fatalf("want %#v, got %#v %v", want, got, args)
	}
}

func (requireT) Len(t *testing.T, v any, n int, args ...any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	if !rv.IsValid() && n == 0 {
		return
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Map && rv.Kind() != reflect.String {
		t.Fatalf("Len: unsupported kind %v %v", rv.Kind(), args)
	}
	if rv.Len() != n {
		t.Fatalf("want length %d, got %d %v", n, rv.Len(), args)
	}
}

func (requireT) NotNil(t *testing.T, v any, args ...any) {
	t.Helper()
	if v == nil || (reflect.ValueOf(v).Kind() == reflect.Ptr && reflect.ValueOf(v).IsNil()) {
		t.Fatalf("want non-nil %v", args)
	}
}

func (requireT) Nil(t *testing.T, v any, args ...any) {
	t.Helper()
	if v == nil {
		return
	}
	// A nil slice or map arrives boxed in a non-nil interface; testify treats
	// those as nil and so must this.
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		if rv.IsNil() {
			return
		}
	}
	t.Fatalf("want nil, got %#v %v", v, args)
}

func (requireT) True(t *testing.T, v bool, args ...any) {
	t.Helper()
	if !v {
		t.Fatalf("want true %v", args)
	}
}

func (requireT) Empty(t *testing.T, v any, args ...any) {
	t.Helper()
	if !isEmptyValue(v) {
		t.Fatalf("want empty, got %#v %v", v, args)
	}
}

func (requireT) NotEmpty(t *testing.T, v any, args ...any) {
	t.Helper()
	if isEmptyValue(v) {
		t.Fatalf("want non-empty %v", args)
	}
}

func (requireT) Contains(t *testing.T, haystack, needle any, args ...any) {
	t.Helper()
	h, okH := haystack.(string)
	n, okN := needle.(string)
	if okH && okN {
		if !strings.Contains(h, n) {
			t.Fatalf("%q does not contain %q %v", h, n, args)
		}
		return
	}
	rv := reflect.ValueOf(haystack)
	if rv.Kind() == reflect.Slice {
		for i := 0; i < rv.Len(); i++ {
			if equalValues(rv.Index(i).Interface(), needle) {
				return
			}
		}
	}
	t.Fatalf("%#v does not contain %#v %v", haystack, needle, args)
}

func (requireT) GreaterOrEqual(t *testing.T, a, b any, args ...any) {
	t.Helper()
	if toFloat(a) < toFloat(b) {
		t.Fatalf("want %v >= %v %v", a, b, args)
	}
}

func (requireT) JSONEq(t *testing.T, want, got string, args ...any) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("got is not JSON: %v", err)
	}
	if !reflect.DeepEqual(w, g) {
		t.Fatalf("JSON differs:\nwant %s\ngot  %s %v", want, got, args)
	}
}

func equalValues(want, got any) bool {
	if reflect.DeepEqual(want, got) {
		return true
	}
	// testify compares numbers and []byte/string leniently enough for this suite.
	if isNumeric(want) && isNumeric(got) {
		return toFloat(want) == toFloat(got)
	}
	wb, wok := want.(json.RawMessage)
	gb, gok := got.(json.RawMessage)
	if wok && gok {
		return string(wb) == string(gb)
	}
	return fmt.Sprint(want) == fmt.Sprint(got) && reflect.TypeOf(want) == reflect.TypeOf(got)
}

func isNumeric(v any) bool {
	switch reflect.ValueOf(v).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func toFloat(v any) float64 {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint())
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	}
	return 0
}

func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.String, reflect.Array:
		return rv.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	}
	return reflect.DeepEqual(v, reflect.Zero(rv.Type()).Interface())
}
