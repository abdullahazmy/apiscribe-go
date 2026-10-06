//go:build !windows

package image

import (
	"reflect"
	"testing"
)

func TestParsePathArgs(t *testing.T) {
	cases := map[string][]string{
		`a.png b.png`:           {"a.png", "b.png"},
		`'/tmp/my shot.png'`:    {"/tmp/my shot.png"},
		`/tmp/my\ shot.png`:     {"/tmp/my shot.png"},
		`file:///tmp/a%20b.png`: {"/tmp/a b.png"},
		`"x y.jpg"  z.webp`:     {"x y.jpg", "z.webp"},
	}
	for in, want := range cases {
		if got := ParsePathArgs(in); !reflect.DeepEqual(got, want) {
			t.Errorf("ParsePathArgs(%q) = %q, want %q", in, got, want)
		}
	}
}
