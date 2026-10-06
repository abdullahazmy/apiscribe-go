// Package prompts holds the system prompt and task prompts. The texts live
// in text/ so they are easy to edit and identical across implementations.
package prompts

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/abdullahazmy/apiscribe-go/internal/config"
)

var (
	//go:embed text/system.md
	systemText string
	//go:embed text/scan.md
	scanText string
	//go:embed text/endpoint.md
	endpointText string
	//go:embed text/image.md
	imageText string
)

// System is byte-stable for a session (no timestamps or per-request data)
// so the prompt cache keeps hitting across turns.
func System(cfg *config.Config) string {
	rel, err := filepath.Rel(cfg.ProjectRoot, cfg.DocsDir)
	if err != nil || rel == "" {
		rel = "."
	}
	return strings.NewReplacer(
		"{{PROJECT_ROOT}}", cfg.ProjectRoot,
		"{{DOCS_DIR}}", cfg.DocsDir,
		"{{DOCS_REL}}", filepath.ToSlash(rel),
	).Replace(systemText)
}

// Scan asks Claude to document the whole API, optionally focused.
func Scan(focus string) string {
	if focus != "" {
		focus = ", focusing on: " + focus
	}
	return strings.ReplaceAll(scanText, "{{FOCUS}}", focus)
}

// Endpoint asks Claude to document a single endpoint.
func Endpoint(target string) string {
	return strings.ReplaceAll(endpointText, "{{TARGET}}", target)
}

// Image asks Claude to map screenshots to API calls.
func Image(names []string, note string) string {
	screens := fmt.Sprintf("This image is a screen from the frontend/mobile app (%s).", strings.Join(names, ", "))
	if len(names) > 1 {
		screens = fmt.Sprintf("These %d images are screens from the frontend/mobile app (%s).", len(names), strings.Join(names, ", "))
	}
	if note != "" {
		note = "\nContext from the developer: " + note
	}
	return strings.NewReplacer("{{SCREENS}}", screens, "{{NOTE}}", note).Replace(imageText)
}
