package config

import (
	"path/filepath"
	"testing"
)

func TestConfine(t *testing.T) {
	root := filepath.FromSlash("/srv/app")
	for _, ok := range []string{"src/main.go", ".", "a/../b"} {
		if _, err := Confine(root, ok); err != nil {
			t.Errorf("Confine(%q) unexpectedly failed: %v", ok, err)
		}
	}
	for _, bad := range []string{"..", "../etc/passwd", "a/../../x", "/etc/passwd"} {
		if _, err := Confine(root, bad); err == nil {
			t.Errorf("Confine(%q) should have failed", bad)
		}
	}
}
