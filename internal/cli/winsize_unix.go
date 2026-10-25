//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// watchResize calls fn with the terminal's new size each time it changes.
func watchResize(fd int, fn func(cols, rows int)) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				if cols, rows, err := term.GetSize(fd); err == nil {
					fn(cols, rows)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
