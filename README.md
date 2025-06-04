# daemon

It doesn't compile, this is just for reference. It references internal go.nozzle.io packages, but should
still be useful to see how the daemon is structured. Was written several years ago, so open to suggestions.

Here's a simple example of something we do internally.

```go
package main

import (
	"context"

	"go.nozzle.io/data-domains/keywords/internal/metrics-batcher/internal"
	"go.nozzle.io/pkg/daemon"
	"go.nozzle.io/pkg/e"
	"go.nozzle.io/pkg/mysql"
	"go.nozzle.io/pkg/workers"
	"go.nozzle.io/pkg/workers/mapqueue"
	"go.nozzle.io/pkg/workers/vitessmessages"
)

func main() {
	daemon.Main(func(c context.Context, d *daemon.Daemon) error {
		db, err := mysql.NewVitessDB(c)
		if err != nil {
			return e.Wrap(err)
		}
		defer db.Close(c)

		refresher := vitessmessages.NewDestinationQueue(c, "keyword_metrics__refresher__msgs",
			vitessmessages.DestinationQueueNoData(),
		)

		app, err := internal.NewApp(c, db, refresher)
		if err != nil {
			return e.Wrap(err)
		}
		defer app.Close(c)

		mq, err := mapqueue.NewIntervalQueue(c, app, app)
		if err != nil {
			return e.Wrap(err)
		}

		d.WithMapQueueHandler(mq.Handler)

		pool, err := workers.NewPool(c,
			workers.WithWorker(mq, 0),
			workers.WithGracefulStopChan(d.GracefulStopChan()),
			workers.AddLowPrioritySourceQueue(mq),
		)
		if err != nil {
			return e.Wrap(err)
		}

		return pool.Run(c)
	})
}

```
