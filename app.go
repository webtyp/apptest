//go:build !wasm

package apptest

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"webtyp.com/devbrowser/chromedp"
	"webtyp.com/env"
	"webtyp.com/postgres"
	"webtyp.com/sitec"
)

// What the generated mains read (appkitc templates): web/server/main.go and migrate/main.go.
const (
	envDatabase       = "DATABASE_URL"
	envSkipTruststore = "WEBTYP_LOCALCERT_SKIP_TRUSTSTORE" // httpd.EnvSkipTruststore: never prompt for sudo in a test
	envDevAutologin   = "DEV_AUTOLOGIN"                    // auth.EnvDevAutologin: emptied, a story signs in like a person
	argPort           = "-server_port="
	argPublicDir      = "-server_public_dir="
	serverEntry       = "./web/server"
	devTag            = "dev"
	healthPath        = "/health"
	databasePrefix    = "apptest_"
	loopbackHost      = "127.0.0.1" // never "localhost": it may resolve to ::1, a device the app never registered
	startTimeout      = 90 * time.Second
	stepTimeout       = 10 * time.Second
)

// migrateEntries are where an app keeps its migration main (project-layout): the first that exists is used.
var migrateEntries = []string{"./migrate", "./cmd/migrate"}

// builtApp is the app compiled once per Run: its server and migration binaries, and its site.
type builtApp struct {
	root, dsn          string
	server, migrate    string
	publicDir, failDir string
}

func buildApp(t *testing.T, appRoot string) *builtApp {
	t.Helper()
	root, err := filepath.Abs(appRoot)
	if err != nil {
		t.Fatalf("apptest: app root %s: %v", appRoot, err)
	}
	dsn, ok := env.LookupAt(envDatabase, filepath.Join(root, ".env"))
	if !ok || dsn == "" {
		t.Fatalf("apptest: no %s for %s — type it in the SECRETS tab of webtyp", envDatabase, root)
	}
	tmp := t.TempDir()
	b := &builtApp{root: root, dsn: dsn, server: filepath.Join(tmp, "server"), migrate: filepath.Join(tmp, "migrate"),
		publicDir: filepath.Join(tmp, "public"), failDir: filepath.Join(os.TempDir(), "apptest-failures")}

	goBuild(t, root, "-tags", devTag, "-o", b.server, serverEntry)
	entry := ""
	for _, e := range migrateEntries {
		if _, err := os.Stat(filepath.Join(root, e)); err == nil {
			entry = e
			break
		}
	}
	if entry == "" {
		t.Fatalf("apptest: %s has no migration main (%s)", root, strings.Join(migrateEntries, " or "))
	}
	goBuild(t, root, "-o", b.migrate, entry)

	out, err := sitec.BuildWithConfig(sitec.BuildConfig{RootDir: root, Mode: sitec.ModeDev, OutputDir: b.publicDir})
	if err != nil {
		t.Fatalf("apptest: site build: %v", err)
	}
	if len(out.LargeFiles()) > 0 {
		t.Fatalf("apptest: the site declares large files; apptest does not place them yet")
	}
	if err := out.WriteTo(sitec.NewOsFS()); err != nil {
		t.Fatalf("apptest: site write: %v", err)
	}
	return b
}

func goBuild(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"build"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apptest: go build %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// RunningApp is the app up for one story: its own empty database (migrated), its own server and its own browser.
type RunningApp struct {
	t       *testing.T
	base    string // https://127.0.0.1:<port>
	port    string
	failDir string

	server *exec.Cmd
	logs   *lockedBuffer
	drop   func() error

	browser       context.Context // first tab: owns the Chrome process
	stopBrowser   context.CancelFunc
	stopAllocator context.CancelFunc
}

func (b *builtApp) start(t *testing.T) *RunningApp {
	t.Helper()
	testDSN, drop, err := postgres.NewEmptyDatabase(b.dsn, databasePrefix)
	if err != nil {
		t.Fatalf("apptest: empty database: %v", err)
	}
	app := &RunningApp{t: t, drop: drop, failDir: b.failDir, logs: &lockedBuffer{}}

	mig := exec.Command(b.migrate)
	mig.Dir = b.root
	mig.Env = append(os.Environ(), envDatabase+"="+testDSN)
	if out, err := mig.CombinedOutput(); err != nil {
		app.stop()
		t.Fatalf("apptest: migration: %v\n%s", err, out)
	}

	port := freePort(t)
	app.base, app.port = "https://"+loopbackHost+":"+port, port
	app.server = exec.Command(b.server, argPort+port, argPublicDir+b.publicDir)
	app.server.Dir = b.root
	app.server.Env = append(os.Environ(), envDatabase+"="+testDSN, envSkipTruststore+"=1", envDevAutologin+"=")
	app.server.Stdout, app.server.Stderr = app.logs, app.logs
	if err := app.server.Start(); err != nil {
		app.stop()
		t.Fatalf("apptest: server start: %v", err)
	}
	if !app.waitHealthy() {
		app.stop()
		t.Fatalf("apptest: server not healthy after %s\n%s", startTimeout, app.logs.String())
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.IgnoreCertErrors, chromedp.Headless, chromedp.NoSandbox)
	alloc, stopAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	first, stopFirst := chromedp.NewContext(alloc)
	if err := chromedp.Run(first); err != nil {
		stopFirst()
		stopAlloc()
		app.stop()
		t.Fatalf("apptest: Chrome: %v", err)
	}
	app.browser, app.stopBrowser, app.stopAllocator = first, stopFirst, stopAlloc
	return app
}

// stop closes the browser, stops the server and drops the database. On a failed story it prints the server log.
func (a *RunningApp) stop() {
	if a.stopBrowser != nil {
		a.stopBrowser()
	}
	if a.stopAllocator != nil {
		a.stopAllocator()
	}
	if a.server != nil && a.server.Process != nil {
		_ = a.server.Process.Kill()
		_ = a.server.Wait()
	}
	if a.t.Failed() {
		a.t.Logf("server log:\n%s", a.logs.String())
	}
	if a.drop != nil {
		if err := a.drop(); err != nil {
			a.t.Errorf("apptest: drop database: %v", err)
		}
	}
}

func (a *RunningApp) waitHealthy() bool {
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if resp, err := insecureClient.Get(a.base + healthPath); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		if a.server.ProcessState != nil {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// networkAddress is the first non-loopback IPv4 address of this machine, "" when it has none. The app sees a browser
// that reaches it through this address as a different device from loopback (every loopback address is one machine to
// the app: input.CanonicalIP).
func networkAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if n, ok := addr.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

// insecureClient asks the app's health route over its local certificate, which no test machine trusts. It is used
// only to know the server is up: every step of a story goes through the browser.
var insecureClient = &http.Client{Timeout: stepTimeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", loopbackHost+":0")
	if err != nil {
		t.Fatalf("apptest: free port: %v", err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// lockedBuffer collects the server output written from its process pipes while the story reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Fatalf fails the story with a message about the app (a prior step the app refused, for instance).
func (a *RunningApp) Fatalf(format string, args ...any) {
	a.t.Helper()
	a.t.Fatalf(format, args...)
}

// Logf writes to the story's log (shown on failure or with -v).
func (a *RunningApp) Logf(format string, args ...any) {
	a.t.Helper()
	a.t.Logf(format, args...)
}
