package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"cloud.google.com/go/profiler"
	"contrib.go.opencensus.io/exporter/prometheus"
	"contrib.go.opencensus.io/exporter/stackdriver"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.opencensus.io/trace"
	"google.golang.org/genproto/googleapis/api/monitoredres"

	"go.nozzle.io/pkg/e"
	"go.nozzle.io/pkg/internal/env"
	"go.nozzle.io/pkg/nstats"
	"go.nozzle.io/pkg/ntrace"
)

func insertDefaultTags(c context.Context) (context.Context, error) {
	var err error

	c, err = tag.New(c,
		tag.Insert(nstats.KeyDataDomain, env.DataDomain),
		tag.Insert(nstats.KeyApp, env.App),
		tag.Insert(nstats.KeyNamespace, env.Namespace),
		tag.Insert(nstats.KeyService, env.App),
	)
	if err != nil {
		return c, e.Wrap(err)
	}

	return c, nil
}

func (d *Daemon) startProfiler(c context.Context) error {
	err := profiler.Start(profiler.Config{
		ProjectID:      env.GoogleProjectID,
		Service:        env.Namespace + "--" + env.App,
		ServiceVersion: env.Version,
		MutexProfiling: true,
	})
	if err != nil {
		return e.Wrap(err)
	}

	return nil
}

func (d *Daemon) registerPrometheusExporter(c context.Context) (http.Handler, error) {
	pe, err := prometheus.NewExporter(prometheus.Options{})
	if err != nil {
		return nil, e.Wrap(err)
	}

	view.RegisterExporter(pe)

	return pe, nil
}

func (d *Daemon) startStackdriverTrace(c context.Context) error {
	exporter, err := stackdriver.NewExporter(stackdriver.Options{
		ProjectID: env.GoogleProjectID,
		// Set a MonitoredResource that represents a GKE container.
		Resource: &monitoredres.MonitoredResource{
			Type: "gke_container",
			Labels: map[string]string{
				"project_id":     env.GoogleProjectID,
				"cluster_name":   env.Cluster,
				"instance_id":    env.Instance,
				"zone":           env.Zone,
				"namespace_id":   env.Namespace,
				"pod_id":         env.PodName,
				"container_name": "app",
			},
		},
		// Set DefaultMonitoringLabels to avoid getting the default "opencensus_task"
		// label. For this to be valid, this exporter should be the only writer
		// to the metrics against this gke_container MonitoredResource. In this case,
		// it means you should only have one process writing to Stackdriver from this
		// container.
		DefaultMonitoringLabels: &stackdriver.Labels{},
	})
	if err != nil {
		return e.Wrap(err, e.Critical())
	}

	trace.RegisterExporter(exporter)

	// check for trace defaults in env, and set if found
	tracePercentStr := strings.TrimSuffix(os.Getenv("TRACE_SAMPLE_PERCENTAGE"), "%")
	if tracePercentStr != "" {
		var tracePercent float64
		tracePercent, err = strconv.ParseFloat(tracePercentStr, 32)
		if err != nil {
			return e.Wrap(err)
		}
		ntrace.SetDefaultProbability(tracePercent / 100)
	}

	trace.ApplyConfig(trace.Config{
		DefaultSampler: ntrace.ProbabilitySampler(),
	})

	// Register so that views are exported.
	view.RegisterExporter(exporter)

	fmt.Printf("daemon: tracing enabled at %0.2f%%\n", ntrace.GetDefaultProbability()*100)

	return nil
}
