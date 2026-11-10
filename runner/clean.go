package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cleanConfig renders a must-gather-clean configuration. Scalars are written
// as JSON strings, which are valid YAML double-quoted scalars.
func cleanConfig(o *ObfuscateOptions) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var b strings.Builder
	b.WriteString("config:\n")

	var obf []string
	if o.IP {
		obf = append(obf, "  - type: IP\n    replacementType: Consistent\n    target: All\n")
	}
	if o.MAC {
		obf = append(obf, "  - type: MAC\n    replacementType: Consistent\n    target: All\n")
	}
	if len(o.Domains) > 0 {
		s := "  - type: Domain\n    replacementType: Consistent\n    target: All\n    domainNames:\n"
		for _, d := range o.Domains {
			s += "    - " + q(d) + "\n"
		}
		obf = append(obf, s)
	}
	if len(o.Keywords) > 0 {
		s := "  - type: Keywords\n    target: All\n    replacement:\n"
		for i, k := range o.Keywords {
			s += fmt.Sprintf("      %s: %s\n", q(k), q(fmt.Sprintf("keyword-%04d", i+1)))
		}
		obf = append(obf, s)
	}
	if len(obf) > 0 {
		b.WriteString("  obfuscate:\n")
		for _, s := range obf {
			b.WriteString(s)
		}
	}

	if o.OmitSecrets {
		b.WriteString(`  omit:
  - type: Kubernetes
    kubernetesResource:
      kind: "Secret"
  - type: Kubernetes
    kubernetesResource:
      kind: CertificateSigningRequest
      apiVersion: certificates.k8s.io/v1
  - type: Kubernetes
    kubernetesResource:
      kind: CertificateSigningRequestList
      apiVersion: certificates.k8s.io/v1
`)
	}
	return b.String()
}

// obfuscate runs must-gather-clean. The report (original -> replacement map)
// goes to reportDir, outside the archive: it must never be sent to anyone.
func (m *manager) obfuscate(ctx context.Context, j *Job, in, out, reportDir string) error {
	cfgPath := filepath.Join(j.dir, "clean-config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cleanConfig(j.Request.Obfuscate)), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		return err
	}
	args := []string{"-c", cfgPath, "-i", in, "-o", out, "-r", reportDir}
	j.log.Printf("$ must-gather-clean %s", strings.Join(args, " "))
	return m.runCmd(ctx, j, m.cfg.cleanPath, args, nil)
}
