package tools

import "testing"

func TestRegisterUniqueIsAtomic(t *testing.T) {
	r := NewRegistry(nil)
	r.Register(&Tool{Name: "existing", Source: "builtin"})
	if err := r.RegisterUnique(&Tool{Name: "new", Source: "mcp:1"}, &Tool{Name: "existing", Source: "mcp:1"}); err == nil {
		t.Fatal("collision accepted")
	}
	if _, ok := r.Get("new"); ok {
		t.Fatal("partially registered batch")
	}
	if err := r.RegisterUnique(&Tool{Name: "duplicate"}, &Tool{Name: "duplicate"}); err == nil {
		t.Fatal("duplicate batch accepted")
	}
	if got, _ := r.Get("existing"); got.Source != "builtin" {
		t.Fatal("original tool replaced")
	}
}
