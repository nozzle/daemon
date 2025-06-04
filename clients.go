package daemon

import (
	"context"
	"fmt"

	er "cloud.google.com/go/errorreporting"
	"github.com/getsentry/sentry-go"

	"go.nozzle.io/pkg/e"
	"go.nozzle.io/pkg/internal/env"
	"go.nozzle.io/pkg/internal/l"
)

func (d *Daemon) initSentry(c context.Context) error {
	// initialize Sentry, but DON'T exit if it doesn't start
	sentryDSN := d.Getenv("SENTRY_DSN1")
	if sentryDSN != "" {
		fmt.Println("daemon: $SENTRY_DSN1 set, initializing client")
		cl, err := sentry.NewClient(sentry.ClientOptions{
			Dsn:         sentryDSN,
			ServerName:  env.Instance,
			Release:     d.Getenv("APP_VERSION_ID"),
			Environment: env.Cluster,
		})
		if err != nil {
			handleError(c, err)
		}
		e.SetSentryClient(c, cl)
		fmt.Println("daemon: successfully initialized Sentry client")
	} else {
		fmt.Println("daemon: $SENTRY_DSN1 not set, skipping client init")
	}

	sentryCriticalDSN := d.Getenv("SENTRY_DSN1_CRITICAL")
	if sentryCriticalDSN != "" {
		fmt.Println("daemon: SENTRY_DSN1_CRITICAL set, initializing client")
		cl, err := sentry.NewClient(sentry.ClientOptions{
			Dsn:         sentryCriticalDSN,
			ServerName:  env.Instance,
			Release:     d.Getenv("APP_VERSION_ID"),
			Environment: env.Cluster,
		})
		if err != nil {
			handleError(c, err)
		}
		e.SetSentryCriticalClient(c, cl)
		fmt.Println("daemon: successfully initialized Sentry critical client")
	} else {
		fmt.Println("daemon: SENTRY_DSN1_CRITICAL not set, skipping client init")
	}

	sentryInfraDSN := d.Getenv("SENTRY_DSN1_INFRA")
	if sentryInfraDSN != "" {
		fmt.Println("daemon: SENTRY_DSN1_INFRA set, initializing client")
		cl, err := sentry.NewClient(sentry.ClientOptions{
			Dsn:         sentryInfraDSN,
			ServerName:  env.Instance,
			Release:     d.Getenv("APP_VERSION_ID"),
			Environment: env.Cluster,
		})
		if err != nil {
			handleError(c, err)
		}
		e.SetSentryInfraClient(c, cl)
		fmt.Println("daemon: successfully initialized Sentry infra client")
	} else {
		fmt.Println("daemon: SENTRY_DSN1_INFRA not set, skipping client init")
	}

	return nil
}

func (d *Daemon) initStackdriverErrorReporting(c context.Context) error {
	fmt.Println("daemon: initializing Stackdriver Error Reporting client")
	// initialize Google Error Reporting client
	errorClient, err := er.NewClient(context.Background(), env.GoogleProjectID, er.Config{
		ServiceName:    env.App,
		ServiceVersion: env.Version,
		OnError: func(err error) {
			l.Error(context.Background(), fmt.Sprintf("Background error from cloud.google.com/go/errorreporting:\n%#v", err))
		},
	})
	if err != nil {
		return e.Wrap(err)
	}

	if err = e.SetStackdriverErrorReportingClient(c, errorClient); err != nil {
		return e.Wrap(err)
	}
	fmt.Println("daemon: successfully initialized Stackdriver Error Reporting client")
	return nil
}
