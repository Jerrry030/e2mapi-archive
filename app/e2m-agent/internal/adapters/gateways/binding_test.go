package gateways

import (
	"context"
	"errors"
	"testing"
)

type testResolver map[string]string

func (r testResolver) ResolveBinding(_ context.Context, id string) (string, error) {
	if value := r[id]; value != "" {
		return value, nil
	}
	return "", errors.New("missing")
}

func TestResolveBindingFailsClosed(t *testing.T) {
	if value, err := ResolveBinding(t.Context(), testResolver{"id": "secret"}, "id"); err != nil || value != "secret" {
		t.Fatalf("resolve = %q err=%v", value, err)
	}
	for _, resolver := range []BindingResolver{nil, testResolver{}} {
		if _, err := ResolveBinding(t.Context(), resolver, "missing"); err == nil {
			t.Fatal("missing binding was accepted")
		}
	}
}
