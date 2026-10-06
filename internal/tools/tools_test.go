package tools

import "testing"

func TestGlobToRegexp(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"**/*.ts", "src/routes/users.ts", true},
		{"**/*.ts", "users.ts", true},
		{"*.ts", "src/users.ts", false},
		{"routes/**", "routes/a/b.js", true},
		{"*.{js,ts}", "app.js", true},
		{"*.{js,ts}", "app.py", false},
		{"user?.go", "users.go", true},
	}
	for _, c := range cases {
		if got := GlobToRegexp(c.glob).MatchString(c.path); got != c.want {
			t.Errorf("glob %q on %q = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}
