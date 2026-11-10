package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCreateTarGz(t *testing.T) {
	src := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(src, "quay-io-image/namespaces/ns1"), 0o700))
	must(t, os.WriteFile(filepath.Join(src, "timestamp"), []byte("now"), 0o600))
	must(t, os.WriteFile(filepath.Join(src, "quay-io-image/namespaces/ns1/ns1.yaml"), []byte("kind: Namespace"), 0o600))
	must(t, os.Symlink("quay-io-image", filepath.Join(src, "latest")))

	dest := filepath.Join(t.TempDir(), "out", "must-gather-x.tar.gz")
	size, err := createTarGz(context.Background(), src, "must-gather.local.123", dest)
	must(t, err)
	if size <= 0 {
		t.Fatalf("size = %d", size)
	}

	f, err := os.Open(dest)
	must(t, err)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	must(t, err)
	tr := tar.NewReader(gz)
	var names []string
	contents := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(t, err)
		if h.Uid != 0 || h.Uname != "" {
			t.Errorf("%s: owner leaked: %d/%q", h.Name, h.Uid, h.Uname)
		}
		names = append(names, h.Name)
		if h.Typeflag == tar.TypeReg {
			b, _ := io.ReadAll(tr)
			contents[h.Name] = string(b)
		}
		if h.Typeflag == tar.TypeSymlink && h.Linkname != "quay-io-image" {
			t.Errorf("symlink target = %q", h.Linkname)
		}
	}
	sort.Strings(names)
	want := []string{
		"must-gather.local.123/",
		"must-gather.local.123/latest",
		"must-gather.local.123/quay-io-image/",
		"must-gather.local.123/quay-io-image/namespaces/",
		"must-gather.local.123/quay-io-image/namespaces/ns1/",
		"must-gather.local.123/quay-io-image/namespaces/ns1/ns1.yaml",
		"must-gather.local.123/timestamp",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("entries:\n got %v\nwant %v", names, want)
	}
	if contents["must-gather.local.123/quay-io-image/namespaces/ns1/ns1.yaml"] != "kind: Namespace" {
		t.Errorf("file content mismatch")
	}
	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Errorf("partial file left behind")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		req     JobRequest
		wantErr string
	}{
		{"full ok", JobRequest{Type: "full"}, ""},
		{"full ignores image", JobRequest{Type: "full", Image: "evil --foo"}, ""},
		{"unknown type", JobRequest{Type: "everything"}, "unknown"},
		{"operator needs image", JobRequest{Type: "storage"}, "required"},
		{"operator bad image", JobRequest{Type: "gitops", Image: "--image=x"}, "invalid image"},
		{"operator ok", JobRequest{Type: "virtualization", Image: "registry.redhat.io/container-native-virtualization/cnv-must-gather-rhel9@sha256:abc"}, ""},
		{"bad case", JobRequest{Type: "full", CaseNumber: "123"}, "8 digits"},
		{"good case", JobRequest{Type: "full", CaseNumber: "04012345"}, ""},
		{"empty obfuscation", JobRequest{Type: "full", Obfuscate: &ObfuscateOptions{Domains: []string{" "}}}, "nothing is selected"},
		{"bad domain", JobRequest{Type: "full", Obfuscate: &ObfuscateOptions{Domains: []string{"a b.com"}}}, "invalid domain"},
		{"short keyword", JobRequest{Type: "full", Obfuscate: &ObfuscateOptions{Keywords: []string{"ab"}}}, "keywords"},
		{"good obfuscation", JobRequest{Type: "full", Obfuscate: &ObfuscateOptions{IP: true, Domains: []string{"Example.COM"}}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.validate()
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
	r := JobRequest{Type: "full", Image: "x", Obfuscate: &ObfuscateOptions{Domains: []string{" Example.COM ", ""}}}
	must(t, r.validate())
	if r.Image != "" || len(r.Obfuscate.Domains) != 1 || r.Obfuscate.Domains[0] != "example.com" {
		t.Fatalf("normalisation failed: %+v %+v", r, r.Obfuscate)
	}
}

func TestCleanConfig(t *testing.T) {
	got := cleanConfig(&ObfuscateOptions{
		IP: true, MAC: true,
		Domains:     []string{"example.com"},
		Keywords:    []string{`my "secret"`},
		OmitSecrets: true,
	})
	for _, want := range []string{
		"config:\n  obfuscate:\n  - type: IP\n",
		"  - type: MAC\n",
		"    domainNames:\n    - \"example.com\"\n",
		"      \"my \\\"secret\\\"\": \"keyword-0001\"\n",
		"  omit:\n  - type: Kubernetes\n    kubernetesResource:\n      kind: \"Secret\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(cleanConfig(&ObfuscateOptions{IP: true}), "omit:") {
		t.Errorf("omit section rendered when not requested")
	}
}

func TestRingLog(t *testing.T) {
	r := newRingLog(3)
	_, _ = r.Write([]byte("one\ntwo\r\nthr"))
	_, _ = r.Write([]byte("ee\nfour\nfive"))
	got := strings.Join(r.lines(), ",")
	if got != "two,three,four" {
		t.Fatalf("lines = %q", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestHasSubdir(t *testing.T) {
	d := t.TempDir()
	if hasSubdir(d) {
		t.Fatal("empty dir reported as gathered")
	}
	must(t, os.WriteFile(filepath.Join(d, "must-gather.logs"), []byte("error: forbidden"), 0o600))
	if hasSubdir(d) {
		t.Fatal("log file alone reported as gathered")
	}
	must(t, os.Mkdir(filepath.Join(d, "quay-io-image"), 0o700))
	if !hasSubdir(d) {
		t.Fatal("image dir not detected")
	}
}

// Output captured from a real run on a node whose disk was 83% full.
const abortedGatherLog = `2026-11-10T19:56:59.795003027Z [disk usage checker] Started
2026-11-10T19:56:59.796694165Z [disk usage checker] Volume usage percentage: current = 83 ; allowed = 70
2026-11-10T19:56:59.796712134Z [disk usage checker] Disk usage exceeds the volume percentage of 70 for mounted directory, terminating...
2026-11-10T19:56:59.801297519Z /bin/bash: line 33:     3 Killed                  setsid -w bash <<-MUSTGATHER_EOF
2026-11-10T19:56:59.842727603Z Caches written to disk
`

func TestDiskCheckAbort(t *testing.T) {
	d := t.TempDir()
	img := filepath.Join(d, "quay-io-image")
	must(t, os.Mkdir(img, 0o700))

	must(t, os.WriteFile(filepath.Join(img, "gather.logs"), []byte(
		"[disk usage checker] Started\n[disk usage checker] Volume usage percentage: current = 40 ; allowed = 70\ngathering...\n"), 0o600))
	if err := diskCheckAbort(d); err != nil {
		t.Fatalf("healthy run reported as aborted: %v", err)
	}

	must(t, os.WriteFile(filepath.Join(img, "gather.logs"), []byte(abortedGatherLog), 0o600))
	err := diskCheckAbort(d)
	if err == nil || !strings.Contains(err.Error(), "83%, above the 70% limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateVolumePercentage(t *testing.T) {
	for _, tc := range []struct {
		v  int
		ok bool
	}{{0, true}, {85, true}, {90, true}, {91, false}, {100, false}, {-1, false}} {
		r := JobRequest{Type: "full", VolumePercentage: tc.v}
		if err := r.validate(); (err == nil) != tc.ok {
			t.Errorf("volumePercentage=%d: err=%v", tc.v, err)
		}
	}
}
