package daemon

import (
	"context"
	"fmt"
	"os"

	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"

	"go.nozzle.io/pkg/e"
	"go.nozzle.io/pkg/internal/env"
	"go.nozzle.io/pkg/internal/l"
	"go.nozzle.io/pkg/l/stackdriverlogger"
	"go.nozzle.io/pkg/l/writerlogger"
)

// An Option provides specific runtime parameters
type Option func(c context.Context, d *Daemon) error

// WithStdoutLogger sends logs to stdout instead of to Stackdriver
func WithStdoutLogger() Option {
	return func(c context.Context, d *Daemon) error {
		d.useCustomLogger = true
		l.SetBackgroundLogger(c, writerlogger.New(c, "stdout", os.Stdout))
		fmt.Println("daemon: using custom logger - stdout")
		return nil
	}
}

// WithStackdriverLogger sends logs to stdout instead of to Stackdriver
func WithStackdriverLogger() Option {
	return func(c context.Context, d *Daemon) error {
		d.useCustomLogger = true

		fmt.Println("daemon: initializing Stackdriver Logging")

		commonLabels := map[string]string{
			"data_domain":    env.DataDomain,
			"app_version_id": env.AppVersionID,
			"image_tag":      env.ImageTag,
		}

		res := &mrpb.MonitoredResource{
			Type: "k8s_container",
			Labels: map[string]string{
				"project_id":     env.GoogleProjectID,
				"cluster_name":   env.Cluster,
				"container_name": env.App,
				"location":       env.Region,
				"namespace_name": env.Namespace,
				"pod_name":       env.PodName,
			},
		}

		parentLogger, err := stackdriverlogger.New(c, "nz_log", commonLabels, res)
		if err != nil {
			return e.Wrap(err)
		}

		// don't include common labels on child logs to save ingestion costs
		childLogger, err := stackdriverlogger.New(c, "nz_log_entries", commonLabels, res)
		if err != nil {
			return e.Wrap(err)
		}

		backgroundLogger, err := stackdriverlogger.New(c, "nz_background", commonLabels, res)
		if err != nil {
			return e.Wrap(err)
		}

		l.SetParentAndChildLoggers(c, parentLogger, childLogger)
		l.SetBackgroundLogger(c, backgroundLogger)

		fmt.Println("daemon: successfully initialized Stackdriver Logging")
		return nil
	}
}
