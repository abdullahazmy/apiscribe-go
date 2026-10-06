// Package image loads screenshots from files or the system clipboard.
package image

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"

	"github.com/anthropics/anthropic-sdk-go"
)

const maxBytes = 5 << 20 // API limit per image

var extTypes = map[string]anthropic.BetaBase64ImageSourceMediaType{
	".png":  anthropic.BetaBase64ImageSourceMediaTypeImagePNG,
	".jpg":  anthropic.BetaBase64ImageSourceMediaTypeImageJPEG,
	".jpeg": anthropic.BetaBase64ImageSourceMediaTypeImageJPEG,
	".gif":  anthropic.BetaBase64ImageSourceMediaTypeImageGIF,
	".webp": anthropic.BetaBase64ImageSourceMediaTypeImageWebP,
}

// Loaded is an image ready to attach to a message.
type Loaded struct {
	Name  string
	Block anthropic.BetaContentBlockParamUnion
}

// IsImagePath reports whether p has a supported image extension.
func IsImagePath(p string) bool {
	_, ok := extTypes[strings.ToLower(filepath.Ext(p))]
	return ok
}

// ParsePathArgs splits typed or pasted text into paths, handling terminal
// drag-and-drop forms: 'quoted', "double quoted", backslash-escaped spaces,
// file:// URLs, and ~/.
func ParsePathArgs(input string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	runes := []rune(input)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\' && runtime.GOOS != "windows" && i+1 < len(runes):
			i++
			cur.WriteRune(runes[i])
		case unicode.IsSpace(c):
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	home, _ := os.UserHomeDir()
	for i, p := range out {
		if strings.HasPrefix(p, "file://") {
			if u, err := url.Parse(p); err == nil {
				p = u.Path
			}
		}
		if strings.HasPrefix(p, "~/") && home != "" {
			p = filepath.Join(home, p[2:])
		}
		out[i] = p
	}
	return out
}

// LoadFile reads an image file.
func LoadFile(file string) (Loaded, error) {
	mt, ok := extTypes[strings.ToLower(filepath.Ext(file))]
	if !ok {
		return Loaded{}, fmt.Errorf("%s: unsupported image type (use png, jpg, gif or webp)", file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return Loaded{}, fmt.Errorf("%s: file not found", file)
	}
	return build(filepath.Base(file), data, mt)
}

// LoadClipboard reads a PNG from the system clipboard (Wayland, X11, macOS, Windows).
func LoadClipboard() (Loaded, error) {
	data := readClipboard()
	if len(data) == 0 {
		return Loaded{}, errors.New("no image found on the clipboard. Copy a screenshot first, or pass a file path: /image ./screen.png")
	}
	return build("clipboard.png", data, anthropic.BetaBase64ImageSourceMediaTypeImagePNG)
}

func build(name string, data []byte, mt anthropic.BetaBase64ImageSourceMediaType) (Loaded, error) {
	if len(data) > maxBytes {
		return Loaded{}, fmt.Errorf("%s is %.1fMB; images must be under 5MB. Resize or crop it first", name, float64(len(data))/(1<<20))
	}
	return Loaded{
		Name: name,
		Block: anthropic.NewBetaImageBlock(anthropic.BetaBase64ImageSourceParam{
			Data:      base64.StdEncoding.EncodeToString(data),
			MediaType: mt,
		}),
	}, nil
}

func output(name string, args ...string) []byte {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return nil
	}
	return out
}

func readClipboard() []byte {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("apiscribe-clip-%d.png", os.Getpid()))
	defer os.Remove(tmp)

	switch runtime.GOOS {
	case "darwin":
		if b := output("pngpaste", "-"); len(b) > 0 {
			return b
		}
		_ = exec.Command("osascript",
			"-e", fmt.Sprintf(`set f to open for access POSIX file %q with write permission`, tmp),
			"-e", "write (the clipboard as «class PNGf») to f",
			"-e", "close access f").Run()
		b, _ := os.ReadFile(tmp)
		return b
	case "windows":
		_ = exec.Command("powershell", "-NoProfile", "-Command",
			fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms; $i=[System.Windows.Forms.Clipboard]::GetImage(); if ($i) { $i.Save('%s') }`, tmp)).Run()
		b, _ := os.ReadFile(tmp)
		return b
	}

	// Linux: Wayland first, then X11.
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		types := strings.Split(string(output("wl-paste", "--list-types")), "\n")
		if slices.Contains(types, "image/png") {
			if b := output("wl-paste", "--type", "image/png"); len(b) > 0 {
				return b
			}
		}
	}
	b := output("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	if !bytes.HasPrefix(b, []byte("\x89PNG")) {
		return nil
	}
	return b
}
