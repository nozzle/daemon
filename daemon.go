package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"go.nozzle.io/pkg/e"
	"go.nozzle.io/pkg/internal/l"
	"go.nozzle.io/pkg/l/blackholelogger"
	"go.nozzle.io/pkg/secretmanager"
)

// Daemon stores details about the daemon process
type Daemon struct {
	// root context
	ctx        context.Context
	cancelFunc context.CancelCauseFunc

	mu sync.Mutex

	// graceful stop options
	osSignalChan      chan os.Signal
	shutdownChan      chan struct{}
	shutdownOnce      sync.Once
	gracefulStopFuncs []HookFunc
	gracefulStopChan  chan struct{}
	gracefulStopDur   time.Duration

	// config options
	useProfiler                  bool
	useSentry                    bool
	useStackdriverErrorReporting bool
	useOpenCensus                bool

	// contextCancel options
	contextCancelDur time.Duration

	// exit options
	exitChan chan struct{}

	gs      *grpc.Server
	handler http.Handler

	// server that provides k8s integrations for health checks
	startInternalServer   sync.Once
	startInternalServerFn func()

	secretManagerClient *secretmanager.Client

	internalServer internalServer

	// this is set to true if a custom logger was set with a daemon option
	useCustomLogger bool
}

// A HookFunc executes user code at the specified place in the code
type HookFunc func(c context.Context) error

// Main provides a hook for global injection points for tracing, stats, etc
func Main(fn func(c context.Context, d *Daemon) error, opts ...Option) {
	fmt.Println("daemon: executing Main()")

	// create the main context with a CancelFunc
	bg := context.Background()
	c, cancelFunc := context.WithCancelCause(bg)

	// create default daemon object
	d := &Daemon{
		ctx:                          c,
		cancelFunc:                   cancelFunc,
		osSignalChan:                 make(chan os.Signal),
		shutdownChan:                 make(chan struct{}),
		gracefulStopDur:              3 * time.Second,
		gracefulStopChan:             make(chan struct{}),
		contextCancelDur:             1 * time.Second,
		useProfiler:                  true,
		useSentry:                    true,
		useStackdriverErrorReporting: true,
		useOpenCensus:                true,
		exitChan:                     make(chan struct{}),
		startInternalServer:          sync.Once{},
		internalServer: internalServer{
			mu:        sync.RWMutex{},
			isHealthy: true,
		},
	}

	err := d.run(c, fn, opts)
	if err != nil {
		handleError(c, err)
	}

	// exit with the error status code
	errCode := int(e.CodeFromError(err))
	fmt.Println("daemon: exiting with error code:", errCode)
	os.Exit(errCode)
}

func (d *Daemon) run(c context.Context, fn func(c context.Context, d *Daemon) error, opts []Option) error {
	// make sure that all logs are flushed before the server shuts down
	defer flush(c)

	var err error

	// set default env vars, exit on failure
	if err = initEnv(c); err != nil {
		return e.Wrap(err)
	}

	// execute functional options
	for _, opt := range opts {
		if err = opt(c, d); err != nil {
			return e.Wrap(err)
		}
	}

	if !d.useCustomLogger {
		l.SetBackgroundLogger(c, blackholelogger.NewBlackholeLogger())
		fmt.Println("daemon: using blackhole logger")
	}

	// load the secret manager client
	// DO EXIT if we can't load it because we require secrets to run properly
	d.secretManagerClient, err = secretmanager.NewClient(d.ctx)
	if err != nil {
		return e.Wrap(err)
	}

	// initialize Sentry, but DON'T exit if it doesn't start
	if d.useSentry {
		if err = d.initSentry(c); err != nil {
			handleError(c, err)
		}
	}

	// initialize Stackdriver Error Reporting, but DON'T exit if it doesn't start
	if d.useStackdriverErrorReporting {
		if err = d.initStackdriverErrorReporting(c); err != nil {
			handleError(c, err)
		}
	}

	// start the profiler, but DON'T exit if it doesn't start
	if d.useProfiler {
		if err = d.startProfiler(c); err != nil {
			handleError(c, err)
		}
	}

	// start OpenCensus, but DON'T exit if it doesn't start
	if d.useOpenCensus {
		if err = d.startStackdriverTrace(c); err != nil {
			handleError(c, err)
		}

		d.internalServer.prometheusHandler, err = d.registerPrometheusExporter(c)
		if err != nil {
			handleError(c, err)
		}
	}

	// Listen for syscalls
	signal.Notify(d.osSignalChan, syscall.SIGTERM, syscall.SIGINT)

	// provide the internal server a parent reference to the daemon
	d.internalServer.d = d

	// start the http server
	d.startInternalServerFn = func() {
		go d.internalServer.listenAndServe(c)
	}

	// start listening for termination signals
	go d.lifecycle(c)

	// start health server
	d.startInternalServer.Do(d.startInternalServerFn)

	// add OpenCensus context tags
	c, err = insertDefaultTags(c)
	if err != nil {
		handleError(c, err)
	}

	fmt.Printf("daemon: running user function\n\n-------------------------------------------------------\n\n")

	// actually run the user func, exiting when they do
	err = fn(c, d)
	fmt.Printf("\n\n-------------------------------------------------------\n\n")
	if err != nil {
		fmt.Println("daemon: user function exited with error:", err.Error())
		handleError(c, err)
	} else {
		fmt.Println("daemon: user function completed without error")
	}

	// trigger the lifecycle shutdown
	fmt.Println("daemon: shutdown called after user function returned")
	d.shutdown(c)

	// wait for the lifecycle to complete
	fmt.Println("daemon: waiting for exitChan to close")
	<-d.exitChan

	return err
}

func flush(c context.Context) {
	wg := sync.WaitGroup{}
	wg.Add(2)

	go func() {
		l.Flush(c)
		wg.Done()
	}()

	go func() {
		e.Flush(c)
		wg.Done()
	}()

	wg.Wait()
}

func handleError(c context.Context, handlerErr error) {
	// add tags, log and set status code if it is an e.Err
	err, ok := handlerErr.(*e.Err)
	if ok {
		// actually write out the log, since none were created prior
		switch err.Level {
		case e.LevelCritical:
			l.Critical(c, err.Error())

		case e.LevelError:
			l.Error(c, err.Error())

		case e.LevelWarning:
			l.Warning(c, err.Error())
		}
	} else {
		l.Error(c, handlerErr.Error())
	}

	// generate and ship out the error data
	e.Wrap(handlerErr).Report(c, e.ReportIsHandler(), e.Wait())

	fmt.Println("daemon: handleError:", e.Cause(handlerErr).Error())
}

// GracefulStopChan returns the internal stop channel for when processes
// need to start shutting down
func (d *Daemon) GracefulStopChan() <-chan struct{} {
	return d.gracefulStopChan
}
