package export

import "testing"

func TestSlugifyMatchesGitHub(t *testing.T) {
	cases := map[string]string{
		"POST /api/orders":        "post-apiorders",
		"GET /users/{id}":         "get-usersid",
		"200 OK":                  "200-ok",
		"Authentication & Tokens": "authentication--tokens",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRewriteLinks(t *testing.T) {
	known := map[string]bool{"README.md": true, "endpoints/auth.md": true}
	d := docFile{rel: "endpoints/auth.md", body: "[home](../README.md) [login](auth.md#post-login) [ext](https://x.dev/a.md)"}
	want := "[home](#doc-readme) [login](#post-login) [ext](https://x.dev/a.md)"
	if got := rewriteLinks(d, known); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
