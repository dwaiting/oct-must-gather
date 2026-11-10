// Command mg-runner is the backend for the OCT Must-Gather console plugin.
//
// The console proxies plugin requests to this service with the signed-in
// user's bearer token (ConsolePlugin proxy authorization: UserToken). Every
// cluster call, including `oc adm must-gather`, is made with that token, so the
// runner's own ServiceAccount needs no RBAC: a user can only gather what they
// could gather with `oc` themselves (in practice, cluster-admin).
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type config struct {
	listenAddr string
	tlsCert    string
	tlsKey     string
	workDir    string
	apiServer  string
	caFile     string
	ocPath     string
	cleanPath  string
	jobTTL     time.Duration
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	ttl, err := time.ParseDuration(envOr("JOB_TTL", "4h"))
	if err != nil || ttl <= 0 {
		ttl = 4 * time.Hour
	}
	apiServer := os.Getenv("KUBE_API_SERVER")
	if apiServer == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" || port == "" {
			apiServer = "https://kubernetes.default.svc"
		} else {
			apiServer = "https://" + joinHostPort(host, port)
		}
	}
	return config{
		listenAddr: envOr("LISTEN_ADDR", ":8443"),
		tlsCert:    envOr("TLS_CERT_FILE", "/var/serving-cert/tls.crt"),
		tlsKey:     envOr("TLS_KEY_FILE", "/var/serving-cert/tls.key"),
		workDir:    envOr("WORK_DIR", "/var/lib/must-gather"),
		apiServer:  apiServer,
		caFile:     envOr("KUBE_CA_FILE", "/var/run/kube-root-ca/ca.crt"),
		ocPath:     envOr("OC_PATH", "oc"),
		cleanPath:  envOr("MUST_GATHER_CLEAN_PATH", "must-gather-clean"),
		jobTTL:     ttl,
	}
}

func joinHostPort(host, port string) string {
	for _, c := range host {
		if c == ':' { // IPv6 literal
			return "[" + host + "]:" + port
		}
	}
	return host + ":" + port
}

func main() {
	cfg := loadConfig()

	// Work from a clean slate: in-memory job state does not survive a restart.
	if err := os.RemoveAll(filepath.Join(cfg.workDir, "jobs")); err != nil {
		log.Printf("warning: clearing stale jobs: %v", err)
	}
	for _, d := range []string{filepath.Join(cfg.workDir, "jobs"), filepath.Join(cfg.workDir, "home")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			log.Fatalf("creating %s: %v", d, err)
		}
	}

	kube, err := newKubeClient(cfg.apiServer, cfg.caFile)
	if err != nil {
		log.Fatalf("kube client: %v", err)
	}
	mgr := newManager(cfg, kube)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go mgr.janitor(ctx)

	srv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           newServer(mgr, kube).routes(),
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		<-ctx.Done()
		log.Print("shutting down: cancelling running jobs")
		mgr.cancelAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("mg-runner listening on %s (api server %s)", cfg.listenAddr, cfg.apiServer)
	if err := srv.ListenAndServeTLS(cfg.tlsCert, cfg.tlsKey); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	mgr.waitAll(25 * time.Second)
}
