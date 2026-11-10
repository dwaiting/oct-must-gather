package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Gather types. Operator types run the must-gather image the operator ships
// (detected by the UI from its ClusterServiceVersion, or entered by the user).
// The others use the cluster's default must-gather image (openshift/must-gather
// ImageStream), optionally with a different gather script.
type gatherType struct {
	operator bool
	command  []string
}

var gatherTypes = map[string]gatherType{
	"full":           {},
	"networking":     {command: []string{"/usr/bin/gather_network_logs"}},
	"storage":        {operator: true},
	"virtualization": {operator: true},
	"serverless":     {operator: true},
	"gitops":         {operator: true},
	"servicemesh":    {operator: true},
}

const auditCommand = "/usr/bin/gather_audit_logs"

type ObfuscateOptions struct {
	IP          bool     `json:"ip"`
	MAC         bool     `json:"mac"`
	Domains     []string `json:"domains"`
	Keywords    []string `json:"keywords"`
	OmitSecrets bool     `json:"omitSecrets"`
}

type JobRequest struct {
	Type       string            `json:"type"`
	Image      string            `json:"image,omitempty"`
	AuditLogs  bool              `json:"auditLogs"`
	Obfuscate  *ObfuscateOptions `json:"obfuscate,omitempty"`
	CaseNumber string            `json:"caseNumber,omitempty"`
	// VolumePercentage raises oc's --volume-percentage (default 70) when the
	// user chose "Run anyway" on a node whose disk is already above 70% used.
	// 0 = oc default. Never above maxVolumePercentage.
	VolumePercentage int `json:"volumePercentage,omitempty"`
}

// maxVolumePercentage caps the must-gather disk limit: above this the gather
// can fill the node disk and affect everything else on the node.
const maxVolumePercentage = 90

var (
	imageRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,511}$`)
	domainRe  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)
	caseNumRe = regexp.MustCompile(`^[0-9]{8}$`)
)

func (r *JobRequest) validate() error {
	gt, ok := gatherTypes[r.Type]
	if !ok {
		return fmt.Errorf("unknown must-gather type %q", r.Type)
	}
	r.Image = strings.TrimSpace(r.Image)
	if gt.operator {
		if r.Image == "" {
			return fmt.Errorf("a must-gather image is required for %s", r.Type)
		}
		if !imageRe.MatchString(r.Image) {
			return fmt.Errorf("invalid image reference %q", r.Image)
		}
	} else {
		r.Image = "" // default cluster image
	}
	r.CaseNumber = strings.TrimSpace(r.CaseNumber)
	if r.CaseNumber != "" && !caseNumRe.MatchString(r.CaseNumber) {
		return fmt.Errorf("support case number must be 8 digits")
	}
	if r.VolumePercentage < 0 || r.VolumePercentage > maxVolumePercentage {
		return fmt.Errorf("node disk usage limit must be between 1 and %d percent", maxVolumePercentage)
	}
	if o := r.Obfuscate; o != nil {
		var domains, keywords []string
		for _, d := range o.Domains {
			d = strings.ToLower(strings.TrimSpace(d))
			if d == "" {
				continue
			}
			if !domainRe.MatchString(d) || len(d) > 253 {
				return fmt.Errorf("invalid domain %q", d)
			}
			domains = append(domains, d)
		}
		for _, k := range o.Keywords {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if len(k) < 3 || len(k) > 256 || strings.ContainsAny(k, "\r\n") {
				return fmt.Errorf("keywords must be 3-256 characters on a single line")
			}
			keywords = append(keywords, k)
		}
		o.Domains, o.Keywords = domains, keywords
		if !o.IP && !o.MAC && !o.OmitSecrets && len(o.Domains) == 0 && len(o.Keywords) == 0 {
			return fmt.Errorf("obfuscation is enabled but nothing is selected to obfuscate")
		}
	}
	return nil
}

type Phase string

const (
	PhasePending     Phase = "Pending"
	PhaseGathering   Phase = "Gathering"
	PhaseObfuscating Phase = "Obfuscating"
	PhaseArchiving   Phase = "Archiving"
	PhaseSucceeded   Phase = "Succeeded"
	PhaseFailed      Phase = "Failed"
	PhaseCanceled    Phase = "Canceled"
)

func (p Phase) done() bool {
	return p == PhaseSucceeded || p == PhaseFailed || p == PhaseCanceled
}

type Job struct {
	ID      string
	Owner   string
	Request JobRequest
	dir     string

	mu          sync.Mutex
	phase       Phase
	message     string
	warning     string
	created     time.Time
	finished    time.Time
	archiveName string
	archivePath string
	archiveSize int64
	reportPath  string
	log         *ringLog
	cancel      context.CancelFunc
	canceled    bool
	doneCh      chan struct{}
}

type JobStatus struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Image       string     `json:"image,omitempty"`
	AuditLogs   bool       `json:"auditLogs"`
	Obfuscated  bool       `json:"obfuscated"`
	CaseNumber  string     `json:"caseNumber,omitempty"`
	Phase       Phase      `json:"phase"`
	Message     string     `json:"message,omitempty"`
	Warning     string     `json:"warning,omitempty"`
	Created     time.Time  `json:"created"`
	Finished    *time.Time `json:"finished,omitempty"`
	ArchiveName string     `json:"archiveName,omitempty"`
	ArchiveSize int64      `json:"archiveSize,omitempty"`
	HasReport   bool       `json:"hasReport"`
	Log         []string   `json:"log"`
}

func (j *Job) status() JobStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := JobStatus{
		ID:          j.ID,
		Type:        j.Request.Type,
		Image:       j.Request.Image,
		AuditLogs:   j.Request.AuditLogs,
		Obfuscated:  j.Request.Obfuscate != nil,
		CaseNumber:  j.Request.CaseNumber,
		Phase:       j.phase,
		Message:     j.message,
		Warning:     j.warning,
		Created:     j.created,
		ArchiveName: j.archiveName,
		ArchiveSize: j.archiveSize,
		HasReport:   j.reportPath != "",
		Log:         tail(j.log.lines(), 500),
	}
	if !j.finished.IsZero() {
		f := j.finished
		s.Finished = &f
	}
	return s
}

func tail(lines []string, n int) []string {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

func (j *Job) setPhase(p Phase, msg string) {
	j.mu.Lock()
	j.phase, j.message = p, msg
	j.mu.Unlock()
	j.log.Printf("==> %s: %s", p, msg)
}

type manager struct {
	cfg  config
	kube *kubeClient

	mu   sync.Mutex
	jobs map[string]*Job
}

func newManager(cfg config, kube *kubeClient) *manager {
	return &manager{cfg: cfg, kube: kube, jobs: map[string]*Job{}}
}

var errBusy = errors.New("another must-gather is already running on this cluster; wait for it to finish or cancel it")

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randomDigits mirrors oc's default must-gather.local.<digits> directory name.
func randomDigits() string {
	n, _ := rand.Int(rand.Reader, new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil))
	return n.String()
}

func (m *manager) start(owner, token string, req JobRequest) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// must-gather is heavy on the API server and nodes: one at a time.
	for _, j := range m.jobs {
		j.mu.Lock()
		running := !j.phase.done()
		j.mu.Unlock()
		if running {
			return nil, errBusy
		}
	}
	id := randomID(8)
	dir := filepath.Join(m.cfg.workDir, "jobs", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{
		ID:      id,
		Owner:   owner,
		Request: req,
		dir:     dir,
		phase:   PhasePending,
		created: time.Now().UTC(),
		log:     newRingLog(2000),
		cancel:  cancel,
		doneCh:  make(chan struct{}),
	}
	m.jobs[id] = j
	log.Printf("job %s: started by %s (type=%s audit=%t obfuscate=%t)", id, owner, req.Type, req.AuditLogs, req.Obfuscate != nil)
	go m.run(ctx, j, token)
	return j, nil
}

func (m *manager) get(owner, id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok && j.Owner == owner {
		return j
	}
	return nil
}

func (m *manager) list(owner string) []JobStatus {
	m.mu.Lock()
	var mine []*Job
	for _, j := range m.jobs {
		if j.Owner == owner {
			mine = append(mine, j)
		}
	}
	m.mu.Unlock()
	out := make([]JobStatus, 0, len(mine))
	for _, j := range mine {
		out = append(out, j.status())
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created.After(out[b].Created) })
	return out
}

// anyRunning reports whether some job (from any user) is active.
func (m *manager) anyRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		j.mu.Lock()
		running := !j.phase.done()
		j.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}

func (j *Job) requestCancel() {
	j.mu.Lock()
	if !j.phase.done() {
		j.canceled = true
	}
	j.mu.Unlock()
	j.cancel()
}

func (m *manager) remove(id string) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	delete(m.jobs, id)
	m.mu.Unlock()
	if !ok {
		return
	}
	j.requestCancel()
	go func() {
		<-j.doneCh
		if err := os.RemoveAll(j.dir); err != nil {
			log.Printf("job %s: removing files: %v", j.ID, err)
		}
	}()
}

func (m *manager) cancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		j.requestCancel()
	}
}

func (m *manager) waitAll(timeout time.Duration) {
	m.mu.Lock()
	var chans []chan struct{}
	for _, j := range m.jobs {
		chans = append(chans, j.doneCh)
	}
	m.mu.Unlock()
	deadline := time.After(timeout)
	for _, c := range chans {
		select {
		case <-c:
		case <-deadline:
			return
		}
	}
}

// janitor deletes finished jobs (and their archives) after the TTL.
func (m *manager) janitor(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var expired []string
		m.mu.Lock()
		for id, j := range m.jobs {
			j.mu.Lock()
			if j.phase.done() && time.Since(j.finished) > m.cfg.jobTTL {
				expired = append(expired, id)
			}
			j.mu.Unlock()
		}
		m.mu.Unlock()
		for _, id := range expired {
			log.Printf("job %s: expired, deleting", id)
			m.remove(id)
		}
	}
}

// run executes gather -> (audit) -> (obfuscate) -> archive.
func (m *manager) run(ctx context.Context, j *Job, token string) {
	defer close(j.doneCh)
	err := m.execute(ctx, j, token)

	j.mu.Lock()
	j.finished = time.Now().UTC()
	switch {
	case err == nil:
		j.phase = PhaseSucceeded
		j.message = "Archive ready to download"
	case j.canceled:
		j.phase = PhaseCanceled
		j.message = "Canceled"
	default:
		j.phase = PhaseFailed
		j.message = err.Error()
	}
	phase, msg := j.phase, j.message
	j.mu.Unlock()
	j.log.Printf("==> %s: %s", phase, msg)
	log.Printf("job %s: %s: %s", j.ID, phase, msg)

	// Do not keep partial data around for failed or canceled runs.
	if phase != PhaseSucceeded {
		_ = os.RemoveAll(filepath.Join(j.dir, "raw"))
		_ = os.RemoveAll(filepath.Join(j.dir, "clean"))
		_ = os.RemoveAll(filepath.Join(j.dir, "report"))
	}
}

func (m *manager) execute(ctx context.Context, j *Job, token string) error {
	req := j.Request
	gt := gatherTypes[req.Type]

	clusterID, err := m.kube.clusterID(ctx, token)
	if err != nil {
		return fmt.Errorf("reading cluster ID: %w", err)
	}
	j.log.Printf("cluster ID: %s", clusterID)

	kubeconfig := filepath.Join(j.dir, "kubeconfig")
	if err := m.kube.writeKubeconfig(kubeconfig, token); err != nil {
		return fmt.Errorf("writing kubeconfig: %w", err)
	}
	defer os.Remove(kubeconfig)

	mgDir := "must-gather.local." + randomDigits()
	rawDir := filepath.Join(j.dir, "raw", mgDir)
	if err := os.MkdirAll(rawDir, 0o700); err != nil {
		return err
	}

	// Pass 1: the selected must-gather.
	var volumeArgs []string
	if req.VolumePercentage > 0 {
		volumeArgs = []string{fmt.Sprintf("--volume-percentage=%d", req.VolumePercentage)}
		j.log.Printf("node disk usage limit raised to %d%% (Run anyway)", req.VolumePercentage)
	}
	args := append([]string{"adm", "must-gather", "--dest-dir=" + rawDir}, volumeArgs...)
	if req.Image != "" {
		args = append(args, "--image="+req.Image)
	}
	if len(gt.command) > 0 {
		args = append(append(args, "--"), gt.command...)
	}
	j.setPhase(PhaseGathering, fmt.Sprintf("Running %s must-gather", req.Type))
	if err := m.oc(ctx, j, kubeconfig, args); err != nil {
		if ctx.Err() != nil || !hasSubdir(rawDir) {
			return fmt.Errorf("must-gather failed: %w", err)
		}
		j.addWarning("must-gather reported errors; the archive may be incomplete (see the log)")
	}
	if err := diskCheckAbort(rawDir); err != nil {
		return err
	}

	// Pass 2: audit logs, with the cluster's default must-gather image.
	if req.AuditLogs {
		j.setPhase(PhaseGathering, "Collecting audit logs")
		args := append(append([]string{"adm", "must-gather", "--dest-dir=" + rawDir}, volumeArgs...), "--", auditCommand)
		if err := m.oc(ctx, j, kubeconfig, args); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("audit log collection failed: %w", err)
			}
			j.addWarning("audit log collection reported errors; audit logs may be incomplete (see the log)")
		}
		if err := diskCheckAbort(rawDir); err != nil {
			return err
		}
	}
	_ = os.Remove(kubeconfig)

	srcRoot := filepath.Join(j.dir, "raw")
	if req.Obfuscate != nil {
		j.setPhase(PhaseObfuscating, "Obfuscating with must-gather-clean")
		cleanRoot := filepath.Join(j.dir, "clean")
		reportDir := filepath.Join(j.dir, "report")
		if err := m.obfuscate(ctx, j, rawDir, filepath.Join(cleanRoot, mgDir), reportDir); err != nil {
			return fmt.Errorf("must-gather-clean failed: %w", err)
		}
		// Never ship the un-obfuscated copy.
		if err := os.RemoveAll(srcRoot); err != nil {
			return err
		}
		srcRoot = cleanRoot
		if _, err := os.Stat(filepath.Join(reportDir, "report.yaml")); err == nil {
			j.mu.Lock()
			j.reportPath = filepath.Join(reportDir, "report.yaml")
			j.mu.Unlock()
		}
	}

	// Red Hat's recommended name: must-gather-<MM-DD-YYYY-HH-MM-SS>-<cluster_id>.tar.gz,
	// prefixed with the support case number (Red Hat SFTP auto-attaches files
	// whose name starts with a case number).
	name := fmt.Sprintf("must-gather-%s-%s.tar.gz", time.Now().UTC().Format("01-02-2006-15-04-05"), clusterID)
	if req.CaseNumber != "" {
		name = req.CaseNumber + "-" + name
	}
	j.setPhase(PhaseArchiving, "Creating "+name)
	archivePath := filepath.Join(j.dir, "out", name)
	size, err := createTarGz(ctx, filepath.Join(srcRoot, mgDir), mgDir, archivePath)
	if err != nil {
		return fmt.Errorf("creating archive: %w", err)
	}
	_ = os.RemoveAll(srcRoot) // the archive is all we keep
	j.mu.Lock()
	j.archiveName, j.archivePath, j.archiveSize = name, archivePath, size
	j.mu.Unlock()
	j.log.Printf("archive %s (%d bytes)", name, size)
	return nil
}

func (j *Job) addWarning(w string) {
	j.mu.Lock()
	if j.warning == "" {
		j.warning = w
	} else {
		j.warning += "; " + w
	}
	j.mu.Unlock()
	j.log.Printf("WARNING: %s", w)
}

func (m *manager) oc(ctx context.Context, j *Job, kubeconfig string, args []string) error {
	j.log.Printf("$ oc %s", strings.Join(args, " "))
	return m.runCmd(ctx, j, m.cfg.ocPath, args, []string{"KUBECONFIG=" + kubeconfig})
}

func (m *manager) runCmd(ctx context.Context, j *Job, path string, args, extraEnv []string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append([]string{
		"HOME=" + filepath.Join(m.cfg.workDir, "home"),
		"PATH=" + os.Getenv("PATH"),
	}, extraEnv...)
	cmd.Dir = j.dir
	cmd.Stdout = j.log
	cmd.Stderr = j.log
	// On cancel, interrupt first: oc then deletes its temporary namespace.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 2 * time.Minute
	return cmd.Run()
}

var diskUsageRe = regexp.MustCompile(`current = (\d+) ; allowed = (\d+)`)

// diskCheckAbort detects must-gather's "disk usage checker" killing the gather
// script. oc still copies whatever was collected and exits 0, so without this
// check a near-empty archive would be reported as a success. The UI blocks
// runs when a candidate node is over the limit; this is the safety net (the
// disk can fill up during the run, or the UI could not read node stats).
func diskCheckAbort(rawDir string) error {
	logs, _ := filepath.Glob(filepath.Join(rawDir, "*", "gather.logs"))
	for _, p := range logs {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		var last []string
		aborted := false
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if m := diskUsageRe.FindStringSubmatch(line); m != nil {
				last = m
			}
			if strings.Contains(line, "[disk usage checker] Disk usage exceeds") {
				aborted = true
				break
			}
		}
		f.Close()
		if !aborted {
			continue
		}
		usage := "above the limit"
		if last != nil {
			usage = fmt.Sprintf("%s%%, above the %s%% limit", last[1], last[2])
		}
		return fmt.Errorf("must-gather stopped early: disk usage on the node running the gather pod is %s, so little or no data was collected. "+
			"Free disk space on that node and run again", usage)
	}
	return nil
}

// hasSubdir reports whether oc gathered anything: it writes one directory per
// image, while files such as must-gather.logs may exist even after an early
// failure (for example missing permissions).
func hasSubdir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			return true
		}
	}
	return false
}
