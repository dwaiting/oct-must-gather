package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// kubeClient makes minimal Kubernetes API calls with a caller-supplied token.
type kubeClient struct {
	server string
	caFile string
	http   *http.Client

	mu    sync.Mutex
	users map[string]cachedUser // sha256(token) -> user
}

type cachedUser struct {
	name    string
	expires time.Time
}

func newKubeClient(server, caFile string) (*kubeClient, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading cluster CA %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates in %s", caFile)
	}
	return &kubeClient{
		server: server,
		caFile: caFile,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
				Proxy:           nil, // in-cluster API: never use an egress proxy
			},
		},
		users: map[string]cachedUser{},
	}, nil
}

type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("API %d: %s", e.status, e.msg) }

func (k *kubeClient) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var st struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &st)
		if st.Message == "" {
			st.Message = http.StatusText(resp.StatusCode)
		}
		return &apiError{status: resp.StatusCode, msg: st.Message}
	}
	return json.Unmarshal(body, out)
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// whoami resolves the user behind a token (cached for a few minutes).
func (k *kubeClient) whoami(ctx context.Context, token string) (string, error) {
	key := tokenKey(token)
	now := time.Now()
	k.mu.Lock()
	if u, ok := k.users[key]; ok && now.Before(u.expires) {
		k.mu.Unlock()
		return u.name, nil
	}
	k.mu.Unlock()

	var user struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if err := k.get(ctx, token, "/apis/user.openshift.io/v1/users/~", &user); err != nil {
		return "", err
	}
	if user.Metadata.Name == "" {
		return "", fmt.Errorf("could not determine user name")
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	for kk, u := range k.users { // keep the cache small
		if now.After(u.expires) {
			delete(k.users, kk)
		}
	}
	k.users[key] = cachedUser{name: user.Metadata.Name, expires: now.Add(5 * time.Minute)}
	return user.Metadata.Name, nil
}

// clusterID returns ClusterVersion spec.clusterID, used in the archive name.
func (k *kubeClient) clusterID(ctx context.Context, token string) (string, error) {
	var cv struct {
		Spec struct {
			ClusterID string `json:"clusterID"`
		} `json:"spec"`
	}
	if err := k.get(ctx, token, "/apis/config.openshift.io/v1/clusterversions/version", &cv); err != nil {
		return "", err
	}
	if cv.Spec.ClusterID == "" {
		return "", fmt.Errorf("ClusterVersion has no spec.clusterID")
	}
	return cv.Spec.ClusterID, nil
}

// writeKubeconfig writes a kubeconfig (mode 0600) that lets oc act as the user.
func (k *kubeClient) writeKubeconfig(path, token string) error {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: cluster
  cluster:
    server: %s
    certificate-authority: %s
users:
- name: user
  user:
    token: %s
contexts:
- name: default
  context:
    cluster: cluster
    user: user
current-context: default
`, q(k.server), q(k.caFile), q(token))
	return os.WriteFile(path, []byte(content), 0o600)
}
