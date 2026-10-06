// Package ui holds terminal styling, the spinner, and status lines.
package ui

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

var color = term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""

func wrap(code, s string) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Style helpers.
func Accent(s string) string { return wrap("38;2;217;119;87", s) }
func Dim(s string) string    { return wrap("2", s) }
func Bold(s string) string   { return wrap("1", s) }
func Red(s string) string    { return wrap("31", s) }
func Green(s string) string  { return wrap("32", s) }
func Yellow(s string) string { return wrap("33", s) }

var frames = []string{"✻", "✼", "✽", "✾", "✿", "❀", "✿", "✾", "✽", "✼"}

// Spinner draws a one-line status on stderr while Claude is working.
type Spinner struct {
	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
}

// Start begins animating; it is a no-op if already running or stderr is not a TTY.
func (s *Spinner) Start(label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil || !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go func(stop, done chan struct{}) {
		defer close(done)
		start := time.Now()
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				fmt.Fprint(os.Stderr, "\r\x1b[K")
				return
			case <-t.C:
				secs := int(time.Since(start).Seconds())
				fmt.Fprintf(os.Stderr, "\r%s %s %s\x1b[K", Accent(frames[i%len(frames)]), Accent(label+"…"),
					Dim(fmt.Sprintf("(%ds · ctrl+c to interrupt)", secs)))
			}
		}
	}(s.stop, s.done)
}

// Stop clears the spinner line.
func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop == nil {
		return
	}
	close(s.stop)
	<-s.done
	s.stop, s.done = nil, nil
}

// ToolLine prints a tool invocation.
func ToolLine(name, detail string) {
	fmt.Printf("%s %s%s%s%s\n", Accent("●"), Bold(name), Dim("("), detail, Dim(")"))
}

// ToolResultLine prints a tool result summary.
func ToolResultLine(text string, isErr bool) {
	if isErr {
		text = Red(text)
	} else {
		text = Dim(text)
	}
	fmt.Printf("  %s  %s\n", Dim("⎿"), text)
}

// Error prints an error line to stderr.
func Error(text string) { fmt.Fprintln(os.Stderr, Red("✗ "+text)) }

// OK prints a success line.
func OK(text string) { fmt.Printf("%s %s\n", Green("✓"), text) }
