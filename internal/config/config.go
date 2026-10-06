// Package config resolves runtime settings and path confinement.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultModel is the Claude model used unless overridden.
const DefaultModel = "claude-opus-5-5"

// Efforts lists the accepted reasoning effort levels.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// Config holds the settings for one session.
type Config struct {
	// ProjectRoot is the backend being documented. All reads are confined here.
	ProjectRoot string
	// DocsDir is where generated Markdown lives. All writes are confined here.
	DocsDir string
	Model   string
	Effort  string
}

// Options are the raw values from flags; empty means "use the default".
type Options struct {
	Project, DocsDir, Model, Effort string
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Resolve applies flags, then environment variables, then defaults.
func Resolve(o Options) (*Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(firstNonEmpty(o.Project, cwd))
	if err != nil {
		return nil, err
	}
	docs := firstNonEmpty(o.DocsDir, os.Getenv("APISCRIBE_DOCS_DIR"), "api-docs")
	if !filepath.IsAbs(docs) {
		docs = filepath.Join(root, docs)
	}
	effort := firstNonEmpty(o.Effort, os.Getenv("APISCRIBE_EFFORT"), "high")
	if !slices.Contains(Efforts, effort) {
		return nil, fmt.Errorf("invalid effort %q; use one of: %s", effort, strings.Join(Efforts, ", "))
	}
	return &Config{
		ProjectRoot: root,
		DocsDir:     filepath.Clean(docs),
		Model:       firstNonEmpty(o.Model, os.Getenv("APISCRIBE_MODEL"), DefaultModel),
		Effort:      effort,
	}, nil
}

// Confine resolves p against root and refuses anything that escapes it.
func Confine(root, p string) (string, error) {
	target := p
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, p)
	}
	target = filepath.Clean(target)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q is outside %s", p, root)
	}
	return target, nil
}
