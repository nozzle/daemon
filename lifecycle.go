package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (d *Daemon) lifecycle(c context.Context) {
	fmt.Println("daemon: launching lifecycle listener")

	var cancelCause string

	select {
	// start shutting down in case we trigger a shutdown internally
	case <-d.shutdownChan:
		cancelCause = "daemon: exiting lifecycle because of internal shutdown"
		fmt.Println(cancelCause)

	// listen on the osSignalChan channel so we can send the proper cancellation signals
	case signal := <-d.osSignalChan:
		cancelCause = "daemon: exiting lifecycle after receiving os signal: " + signal.String()
		fmt.Println(cancelCause)
		d.shutdown(c)
	}

	// signal all listeners to start shutting down
	close(d.gracefulStopChan)

	// run all the graceful stop funcs in parallel
	for i, fn := range d.gracefulStopFuncs {
		go func(stopFn HookFunc, idx int) {
			if err := stopFn(c); err != nil {
				fmt.Println("daemon: graceful stop function failed at index", idx)
				handleError(c, err)
			}
		}(fn, i)
	}
	// wait and give the user time to gracefully shut down before canceling the context
	fmt.Printf("daemon: waiting %s before canceling context\n", d.gracefulStopDur.String())
	d.wait(c, d.gracefulStopDur)

	// cancel the root context and wait for goroutines to return
	fmt.Println("daemon: canceling context")
	d.cancelFunc(errors.New(cancelCause))
	fmt.Printf("daemon: waiting %s before starting exit\n", d.contextCancelDur.String())
	d.wait(c, d.contextCancelDur)

	// close the exit channel, allowing the daemon to exit
	fmt.Println("daemon: closing exitChan")
	close(d.exitChan)
}

// wait returns after either waiting for the provided duration
func (d *Daemon) wait(c context.Context, dur time.Duration) {
	time.Sleep(dur)
}

// shutdown triggers the daemon to shut down
func (d *Daemon) shutdown(c context.Context) {
	d.shutdownOnce.Do(func() {
		d.setHealth(false)
		fmt.Println("daemon: shutdown called")
		close(d.shutdownChan)

		// shut down the server to prevent new connections from being made
		// this does not cancel out active connections
		if d.internalServer.s != nil {
			if err := d.internalServer.s.Shutdown(c); err != nil {
				handleError(c, err)
			}
		}
	})
}

func (d *Daemon) addGracefulStopFunc(fn HookFunc) {
	d.mu.Lock()
	d.gracefulStopFuncs = append(d.gracefulStopFuncs, fn)
	d.mu.Unlock()
}
