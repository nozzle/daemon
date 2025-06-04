package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	"go.nozzle.io/pkg/e"
	"go.nozzle.io/pkg/internal/l"
)

// ServeProxy proxies requests to either grpc or http, based on request headers
func (d *Daemon) ServeProxy(c context.Context, s *grpc.Server, h http.Handler) {
	d.gs = s
	d.handler = h
	d.ServeHTTP(c, http.HandlerFunc(d.proxyHandler))
}

func (d *Daemon) proxyHandler(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
		d.gs.ServeHTTP(w, r)
	} else {
		d.handler.ServeHTTP(w, r)
	}
}

func getPort(envKey string, defaultPort int) string {
	port := os.Getenv(envKey)
	if port == "" {
		port = fmt.Sprintf(":%d", defaultPort)
	}
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	return port
}

// ServeGRPC runs the specified handler in a server, using the daemon lifecycle to manage it
func (d *Daemon) ServeGRPC(c context.Context, s *grpc.Server) {
	// keep a reference to the server in the daemon
	d.gs = s

	lis, err := net.Listen("tcp", getPort("GRPC_PORT", 8888))
	if err != nil {
		panic(err)
	}

	// register the server to be shut down gracefully as the daemon shuts down
	d.addGracefulStopFunc(func(c context.Context) error {
		s.GracefulStop()
		return nil
	})

	// start the grpc server
	fmt.Println("daemon: starting user gRPC server on:", lis.Addr().String())
	err = d.gs.Serve(lis)
	switch err {
	case nil:
	case http.ErrServerClosed:
	default:
		e.Wrap(err).Report(c)
	}

	// trigger the daemon shutdown process
	fmt.Println("daemon: closing user gRPC server & calling shutdown")
	d.shutdown(c)
}

// ServeHTTP runs the specified handler in a server, using the daemon lifecycle to manage it
func (d *Daemon) ServeHTTP(c context.Context, h http.Handler) {
	s := &http.Server{
		Addr:              getPort("APP_PORT", 8080),
		Handler:           h,
		ReadTimeout:       10 * time.Minute,
		WriteTimeout:      10 * time.Minute,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// register the server to be shut down gracefully as the daemon shuts down
	d.addGracefulStopFunc(s.Shutdown)

	// start the http server
	fmt.Println("daemon: starting user http server on:", s.Addr)
	err := s.ListenAndServe()
	switch err {
	case nil:
	case http.ErrServerClosed:
	default:
		e.Wrap(err).Report(c)
	}

	// trigger the daemon shutdown process
	fmt.Println("daemon: closing user http server & calling shutdown")
	d.shutdown(c)
}

// WithMapQueueHandler exposes the mapqueue handler to the internal health server
func (d *Daemon) WithMapQueueHandler(h http.Handler) {
	d.internalServer.mapqueueHandler = http.StripPrefix("/mapqueue", h)
}

type internalServer struct {
	d *Daemon

	mu        sync.RWMutex
	isHealthy bool

	// responds to liveness/readiness checks
	s *http.Server

	// used to determine if the process is alive / health check
	livenessFuncs []HookFunc
	// used to determine if the daemon is ready to serve production traffic
	readinessFuncs []HookFunc

	prometheusHandler http.Handler

	mapqueueHandler http.Handler
}

func (s *internalServer) listenAndServe(c context.Context) {
	s.s = &http.Server{
		Addr:    getPort("HEALTH_PORT", 9090),
		Handler: http.HandlerFunc(s.handler),

		// these timeouts are longer because of the pprof endpoints
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	fmt.Println("daemon: starting internal health server on:", s.s.Addr)
	err := s.s.ListenAndServe()
	switch err {
	case nil:
	case http.ErrServerClosed:
	default:
		e.Wrap(err).Report(c, e.Wait())
		panic(err)
	}

	fmt.Println("daemon: closing internal server")
}

func (s *internalServer) handler(w http.ResponseWriter, r *http.Request) {
	var err error
	path := r.URL.Path
	switch {
	case path == "/metrics" || path == "/metrics/":
		s.prometheusHandler.ServeHTTP(w, r)
		return

	case path == "/_nz/liveness" || path == "/_nz/liveness/":
		err = s.handleHealthCheck(s.d.ctx, s.livenessFuncs)

	case path == "/_nz/readiness" || path == "/_nz/readiness/":
		err = s.handleHealthCheck(s.d.ctx, s.readinessFuncs)

	// this redirects /debug/pprof to /debug/pprof/ so that the relative links to the other pprof pages work
	// the /pprof and /pprof/ paths are also redirected to /debug/pprof/ for convenience
	case path == "/debug/pprof", path == "/pprof", path == "/pprof/":
		http.Redirect(w, r, "/debug/pprof/", http.StatusFound)
		return

	case strings.HasPrefix(path, "/debug/pprof/"):
		pprof.Index(w, r)
		return

	case strings.HasPrefix(path, "/mapqueue"):
		if s.mapqueueHandler == nil {
			err = e.New("mapqueue handler not set")
			break
		}

		s.mapqueueHandler.ServeHTTP(w, r)
		return

	// this is a common path that browsers hit, so we don't want to report errors for it
	case path == "/favicon.ico":
		http.NotFound(w, r)
		return

	default:
		err = e.New("path 404 reached", e.With("404 path", r.URL.Path))
	}

	if err != nil {
		e.Wrap(err).Report(s.d.ctx)
		l.Error(s.d.ctx, err.Error())
		w.WriteHeader(500)
		w.Write([]byte(err.Error())) //nolint:errcheck // we don't care if this fails, it's a best-effort
		return
	}

	w.WriteHeader(200)
}

func (s *internalServer) handleHealthCheck(c context.Context, fns []HookFunc) error {
	// if there are no checks to run, avoid locking the mutex
	if len(fns) == 0 {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isHealthy {
		fmt.Println("daemon: permanently unhealthy")
		return errors.New("daemon: permanently unhealthy")
	}

	var err error
	for i, fn := range fns {
		newCtx, cancelFn := context.WithTimeout(c, 250*time.Millisecond)
		if err = fn(newCtx); err != nil {
			fmt.Println("daemon: health check function failed at index", i)
			cancelFn()
			return err
		}
		cancelFn()
	}
	return nil
}

func (d *Daemon) setHealth(isHealthy bool) {
	d.internalServer.mu.Lock()
	d.internalServer.isHealthy = isHealthy
	d.internalServer.mu.Unlock()
}

// AddLivenessCheck lets you provide a function that will be called to determine
// the health of the server. It will typically be called every 5-10 seconds, and
// after a certain amount of errors will cause the server to restart
func (d *Daemon) AddLivenessCheck(fn HookFunc) {
	d.internalServer.mu.Lock()
	d.internalServer.livenessFuncs = append(d.internalServer.livenessFuncs, fn)
	d.internalServer.mu.Unlock()
}

// AddLivenessCheckErrChan lets you provide an error channel, and any errors
// will cause the check to fail permanently
func (d *Daemon) AddLivenessCheckErrChan(errChan <-chan error) {
	go func() {
		select {
		case err := <-errChan:
			e.Wrap(err).Report(d.ctx)

			d.internalServer.mu.Lock()
			d.internalServer.isHealthy = false
			d.internalServer.mu.Unlock()

		case <-d.ctx.Done():
			return
		}
	}()
}
