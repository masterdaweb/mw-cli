//go:build windows

package cli

import (
	"time"

	"golang.org/x/term"
)

// watchResize calls fn with the console's new size each time it changes: a
// Windows console sends no signal, so it is looked at twice a second.
func watchResize(fd int, fn func(cols, rows int)) (stop func()) {
	done := make(chan struct{})
	go func() {
		lastCols, lastRows, _ := term.GetSize(fd)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if cols, rows, err := term.GetSize(fd); err == nil && (cols != lastCols || rows != lastRows) {
					lastCols, lastRows = cols, rows
					fn(cols, rows)
				}
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
