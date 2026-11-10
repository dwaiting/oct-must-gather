package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

type server struct {
	mgr  *manager
	kube *kubeClient
}

func newServer(mgr *manager, kube *kubeClient) *server { return &server{mgr: mgr, kube: kube} }

type ctxKey struct{}

type caller struct {
	user  string
	token string
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("GET /api/jobs", s.auth(s.listJobs))
	mux.Handle("POST /api/jobs", s.auth(s.createJob))
	mux.Handle("GET /api/jobs/{id}", s.auth(s.getJob))
	mux.Handle("POST /api/jobs/{id}/cancel", s.auth(s.cancelJob))
	mux.Handle("DELETE /api/jobs/{id}", s.auth(s.deleteJob))
	mux.Handle("GET /api/jobs/{id}/archive", s.auth(s.downloadArchive))
	mux.Handle("GET /api/jobs/{id}/report", s.auth(s.downloadReport))
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// auth resolves the console user from the bearer token the console proxy adds.
func (s *server) auth(next func(http.ResponseWriter, *http.Request, caller)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token (open this page from the OpenShift console)")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		user, err := s.kube.whoami(ctx, token)
		if err != nil {
			var ae *apiError
			if errors.As(err, &ae) && (ae.status == http.StatusUnauthorized || ae.status == http.StatusForbidden) {
				writeError(w, http.StatusUnauthorized, "token rejected by the API server")
				return
			}
			log.Printf("whoami: %v", err)
			writeError(w, http.StatusBadGateway, "could not verify the user with the API server")
			return
		}
		next(w, r, caller{user: user, token: token})
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func (s *server) listJobs(w http.ResponseWriter, _ *http.Request, c caller) {
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":        s.mgr.list(c.user),
		"busy":        s.mgr.anyRunning(),
		"retainedFor": s.mgr.cfg.jobTTL.String(),
	})
}

func (s *server) createJob(w http.ResponseWriter, r *http.Request, c caller) {
	var req JobRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	j, err := s.mgr.start(c.user, c.token, req)
	if errors.Is(err, errBusy) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		log.Printf("start job: %v", err)
		writeError(w, http.StatusInternalServerError, "could not start the job")
		return
	}
	writeJSON(w, http.StatusCreated, j.status())
}

func (s *server) job(w http.ResponseWriter, r *http.Request, c caller) *Job {
	j := s.mgr.get(c.user, r.PathValue("id"))
	if j == nil {
		writeError(w, http.StatusNotFound, "job not found (it may have expired)")
	}
	return j
}

func (s *server) getJob(w http.ResponseWriter, r *http.Request, c caller) {
	if j := s.job(w, r, c); j != nil {
		writeJSON(w, http.StatusOK, j.status())
	}
}

func (s *server) cancelJob(w http.ResponseWriter, r *http.Request, c caller) {
	if j := s.job(w, r, c); j != nil {
		j.requestCancel()
		writeJSON(w, http.StatusAccepted, j.status())
	}
}

func (s *server) deleteJob(w http.ResponseWriter, r *http.Request, c caller) {
	if j := s.job(w, r, c); j != nil {
		s.mgr.remove(j.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *server) downloadArchive(w http.ResponseWriter, r *http.Request, c caller) {
	j := s.job(w, r, c)
	if j == nil {
		return
	}
	j.mu.Lock()
	p, name, ready := j.archivePath, j.archiveName, j.phase == PhaseSucceeded
	j.mu.Unlock()
	if !ready || p == "" {
		writeError(w, http.StatusConflict, "the archive is not ready")
		return
	}
	serveFile(w, r, p, name, "application/gzip")
}

func (s *server) downloadReport(w http.ResponseWriter, r *http.Request, c caller) {
	j := s.job(w, r, c)
	if j == nil {
		return
	}
	j.mu.Lock()
	p, name := j.reportPath, strings.TrimSuffix(j.archiveName, ".tar.gz")
	j.mu.Unlock()
	if p == "" {
		writeError(w, http.StatusNotFound, "no obfuscation report for this job")
		return
	}
	serveFile(w, r, p, name+"-obfuscation-report.yaml", "application/yaml")
}

func serveFile(w http.ResponseWriter, r *http.Request, path, name, contentType string) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found (it may have expired)")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read file")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, st.ModTime(), f)
}
