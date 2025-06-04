package daemon

import (
	"context"
	"time"
)

// A TickRunner executes a handler and returns an error
type TickRunner interface {
	Run(c context.Context) error
}

// RunTicker executes the provided TickerFunc at the given interval.
// It adjusts the intervals or drops ticks if the function takes longer
// than the iinterval to execute. It runs until its context is canceled.
func (d *Daemon) RunTicker(c context.Context, interval time.Duration, runner TickRunner) {
	var err error
	t := time.NewTicker(interval)

	for {
		select {
		case <-t.C:
			if err = runner.Run(c); err != nil {
				handleError(c, err)
			}

		case <-c.Done():
			t.Stop()
			return
		}
	}
}
