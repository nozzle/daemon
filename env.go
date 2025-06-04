package daemon

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"

	"go.nozzle.io/pkg/e"
	"go.nozzle.io/pkg/internal/env"
)

func initEnv(c context.Context) error {
	// env vars set by noperator
	env.DataDomain = os.Getenv("DATA_DOMAIN")
	env.App = os.Getenv("APP_ID")
	env.AppVersionID = os.Getenv("APP_VERSION_ID")
	env.ImageTag = os.Getenv("IMAGE_TAG")

	// k8s downward api
	env.Cluster = "nozzle"
	env.Node = os.Getenv("NODE_NAME")
	env.Namespace = os.Getenv("POD_NAMESPACE")
	env.PodName = os.Getenv("POD_NAME")
	env.Instance, _ = os.Hostname()

	env.GoogleProjectID = os.Getenv("GCP_PROJECT_ID")

	// set GCP specific env
	if metadata.OnGCE() {
		zone, err := metadata.Zone()
		if err != nil {
			return e.Wrap(err)
		}

		env.Region = zone[:strings.LastIndex(zone, "-")]
		env.Zone = zone
	}

	return nil
}

// Getenv returns the value for the given key. It is generally a wrapper for os.Getenv,
// with some special cases for zone aware vars and secret retrieval
func (d *Daemon) Getenv(key string) string {
	val := os.Getenv(key)

	if strings.ToUpper(key) == "VTGATE_ADDRESS" {
		return d.getVTGateAddress(val)
	}

	if strings.HasPrefix(val, "gsm://") {
		return d.getSecret(key, val)
	}

	return val
}

func (d *Daemon) getVTGateAddress(envVal string) string {
	// if the hostIP is set, then we can communicate directly to localhost
	hostIP := os.Getenv("HOST_IP")
	if hostIP == "" {
		fmt.Println("daemon: connecting to env defined vtgate:", envVal)
		return envVal
	}

	vtgateAddress := hostIP + ":15991"
	fmt.Println("daemon: connecting to localhost vtgate:", vtgateAddress)
	return vtgateAddress
}

func (d *Daemon) getSecret(key, val string) string {
	// attempt to parse the value as a URL and if it fails, return the original value
	parsedURL, err := url.Parse(val)
	if err != nil {
		return val
	}

	secretName := strings.TrimPrefix(parsedURL.Path, "/")
	secretKey := parsedURL.Query().Get("key")

	secret, _, err := d.secretManagerClient.Secret(d.ctx, "nozzle-app", secretName)
	if err != nil {
		handleError(d.ctx, err)
		return ""
	}

	secretVal, _ := secret.GetValue(secretKey)
	if secretVal == "" {
		handleError(d.ctx, e.New("env var referenced an empty secret key: "+key,
			e.With("key", key), e.With("val", val),
		))
	}

	return secretVal
}

// MustGetenvInt retrieves a value using Getenv and then parses it into an int. Panics on error
func (d *Daemon) MustGetenvInt(key string) int {
	val := d.Getenv(key)
	i, err := strconv.Atoi(val)
	if err != nil {
		handleError(d.ctx, e.Wrap(err, e.Critical(), e.With("key", key), e.With("val", val)))
		panic(err)
	}
	return i
}

// MustGetenvInt32 retrieves a value using Getenv and then parses it into an int32. Panics on error
func (d *Daemon) MustGetenvInt32(key string) int32 {
	val := d.Getenv(key)
	i, err := strconv.ParseInt(val, 10, 32)
	if err != nil {
		handleError(d.ctx, e.Wrap(err, e.Critical(), e.With("key", key), e.With("val", val)))
		panic(err)
	}
	return int32(i)
}

// MustGetenvInt64 retrieves a value using Getenv and then parses it into an int64. Panics on error
func (d *Daemon) MustGetenvInt64(key string) int64 {
	val := d.Getenv(key)
	i, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		handleError(d.ctx, e.Wrap(err, e.Critical(), e.With("key", key), e.With("val", val)))
		panic(err)
	}
	return i
}

// MustGetenvDuration retrieves a value using Getenv and then parses it into a time.Duration. Panics on error
func (d *Daemon) MustGetenvDuration(key string) time.Duration {
	val := d.Getenv(key)
	dur, err := time.ParseDuration(val)
	if err != nil {
		handleError(d.ctx, e.Wrap(err, e.Critical(), e.With("key", key), e.With("val", val)))
		panic(err)
	}
	return dur
}
