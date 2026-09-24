package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	oldVersion := version
	version = "0.1.0"
	defer func() { version = oldVersion }()

	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodHead, "/", http.StatusOK},
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
	} {
		rec := httptest.NewRecorder()
		handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.status {
			t.Errorf("%s %s: status = %d, want %d", tc.method, tc.path, rec.Code, tc.status)
		}
		if tc.path == "/" && tc.status == http.StatusOK {
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("%s /: missing no-store header", tc.method)
			}
			if tc.method == http.MethodGet && !strings.Contains(rec.Body.String(), "version='0.1.0'") {
				t.Error("GET /: script has no release version")
			}
			if tc.method == http.MethodHead && rec.Body.Len() != 0 {
				t.Error("HEAD /: unexpected body")
			}
		}
	}
}

// installerFixture runs the embedded script against local release files.
type installerFixture struct {
	root    string
	home    string
	tmp     string
	bin     string
	log     string
	args    string
	checks  string
	version string
}

func newInstallerFixture(t *testing.T) *installerFixture {
	t.Helper()
	root := t.TempDir()
	f := &installerFixture{
		root:    root,
		home:    filepath.Join(root, "home"),
		tmp:     filepath.Join(root, "tmp"),
		bin:     filepath.Join(root, "bin"),
		log:     filepath.Join(root, "curl.log"),
		args:    filepath.Join(root, "args.log"),
		checks:  filepath.Join(root, "SHA256SUMS"),
		version: "0.1.0",
	}
	for _, dir := range []string{f.home, f.tmp, f.bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fakeFast := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RUN_ARGS\"\nexit \"${BINARY_EXIT:-0}\"\n")
	asset := filepath.Join(root, "asset")
	if err := os.WriteFile(asset, fakeFast, 0700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(fakeFast)
	var sums strings.Builder
	for _, platform := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"} {
		fmt.Fprintf(&sums, "%x  fast-v%s-%s\n", digest, f.version, platform)
	}
	if err := os.WriteFile(f.checks, []byte(sums.String()), 0600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(f.bin, "uname"), `#!/bin/sh
case "$1" in
  -s) printf '%s\n' "$FAKE_OS" ;;
  -m) printf '%s\n' "$FAKE_ARCH" ;;
esac
`)
	writeExecutable(t, filepath.Join(f.bin, "curl"), `#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output=$2; shift 2 ;;
    *) url=$1; shift ;;
  esac
done
printf '%s\n' "$url" >> "$CURL_LOG"
if [ "${FAIL_CURL:-}" = 1 ]; then exit 22; fi
case "$url" in
  */SHA256SUMS) cp "$FIXTURE_SUMS" "$output" ;;
  */fast-v*) cp "$FIXTURE_BINARY" "$output" ;;
  *) exit 22 ;;
esac
`)
	return f
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func (f *installerFixture) run(t *testing.T, system, arch string, args ...string) (int, string) {
	t.Helper()
	contents := strings.ReplaceAll(installerScript, "@VERSION@", f.version)
	cmd := exec.Command("/bin/bash", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = strings.NewReader(contents)
	cmd.Env = append(os.Environ(),
		"HOME="+f.home,
		"TMPDIR="+f.tmp,
		"PATH="+f.bin+":"+os.Getenv("PATH"),
		"FAKE_OS="+system,
		"FAKE_ARCH="+arch,
		"FIXTURE_SUMS="+f.checks,
		"FIXTURE_BINARY="+filepath.Join(f.root, "asset"),
		"CURL_LOG="+f.log,
		"RUN_ARGS="+f.args,
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err == nil {
		return 0, output.String()
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), output.String()
	}
	t.Fatal(err)
	return 0, ""
}

func TestInstallerNoInstall(t *testing.T) {
	for _, tc := range []struct{ system, arch, asset string }{
		{"Linux", "x86_64", "linux-amd64"},
		{"Linux", "aarch64", "linux-arm64"},
		{"Darwin", "x86_64", "darwin-amd64"},
		{"Darwin", "arm64", "darwin-arm64"},
	} {
		t.Run(tc.asset, func(t *testing.T) {
			f := newInstallerFixture(t)
			status, output := f.run(t, tc.system, tc.arch, "--no-install", "--", "--upload")
			if status != 0 {
				t.Fatalf("exit %d: %s", status, output)
			}
			log, err := os.ReadFile(f.log)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(log), "fast-v0.1.0-"+tc.asset) {
				t.Errorf("downloaded wrong asset: %s", log)
			}
			args, err := os.ReadFile(f.args)
			if err != nil {
				t.Fatal(err)
			}
			if string(args) != "--upload\n" {
				t.Errorf("fast args = %q", args)
			}
			if entries, err := os.ReadDir(f.tmp); err != nil || len(entries) != 0 {
				t.Errorf("temporary files remain: %v, %v", entries, err)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".local", "bin", "fast")); !os.IsNotExist(err) {
				t.Errorf("no-install created persistent binary: %v", err)
			}
		})
	}
}

func TestInstallerPersistentAndFailures(t *testing.T) {
	f := newInstallerFixture(t)
	status, output := f.run(t, "Darwin", "arm64")
	if status != 0 {
		t.Fatalf("install exit %d: %s", status, output)
	}
	installed := filepath.Join(f.home, ".local", "bin", "fast")
	before, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("installed mode = %o, want 700", info.Mode().Perm())
	}
	if !bytes.Equal(before, mustRead(t, filepath.Join(f.root, "asset"))) {
		t.Fatal("installed binary differs from verified asset")
	}
	if !strings.Contains(output, "Add "+filepath.Dir(installed)+" to PATH") {
		t.Errorf("missing PATH hint: %s", output)
	}

	if err := os.WriteFile(f.checks, []byte(strings.Repeat("0", 64)+"  fast-v0.1.0-darwin-arm64\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status, output = f.run(t, "Darwin", "arm64")
	if status == 0 || !strings.Contains(output, "checksum mismatch") {
		t.Errorf("checksum failure = exit %d, %s", status, output)
	}
	if !bytes.Equal(before, mustRead(t, installed)) {
		t.Fatal("checksum failure replaced existing binary")
	}
	if entries, err := os.ReadDir(filepath.Dir(installed)); err != nil || len(entries) != 1 {
		t.Errorf("staging files remain: %v, %v", entries, err)
	}
}

func TestInstallerFailureCleanupAndExitStatus(t *testing.T) {
	f := newInstallerFixture(t)
	status, output := f.run(t, "Solaris", "arm64", "--no-install")
	if status == 0 || !strings.Contains(output, "unsupported operating system") {
		t.Errorf("unsupported OS = exit %d, %s", status, output)
	}
	status, output = f.run(t, "Linux", "mips", "--no-install")
	if status == 0 || !strings.Contains(output, "unsupported CPU") {
		t.Errorf("unsupported CPU = exit %d, %s", status, output)
	}
	if err := os.WriteFile(f.checks, []byte("invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status, output = f.run(t, "Linux", "x86_64", "--no-install")
	if status == 0 || !strings.Contains(output, "missing or invalid release checksum") {
		t.Errorf("missing checksum = exit %d, %s", status, output)
	}
	if entries, err := os.ReadDir(f.tmp); err != nil || len(entries) != 0 {
		t.Errorf("temporary files remain: %v, %v", entries, err)
	}
	f = newInstallerFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "asset"), []byte("#!/bin/sh\nexit 23\n"), 0700); err != nil {
		t.Fatal(err)
	}
	newDigest := sha256.Sum256(mustRead(t, filepath.Join(f.root, "asset")))
	if err := os.WriteFile(f.checks, []byte(fmt.Sprintf("%x  fast-v0.1.0-linux-amd64\n", newDigest)), 0600); err != nil {
		t.Fatal(err)
	}
	status, output = f.run(t, "Linux", "x86_64", "--no-install")
	if status != 23 {
		t.Errorf("fast exit status = %d, want 23: %s", status, output)
	}
	if entries, err := os.ReadDir(f.tmp); err != nil || len(entries) != 0 {
		t.Errorf("temporary files remain after fast failure: %v, %v", entries, err)
	}
	f = newInstallerFixture(t)
	t.Setenv("FAIL_CURL", "1")
	status, output = f.run(t, "Linux", "x86_64", "--no-install")
	if status == 0 {
		t.Errorf("failed download succeeded: %s", output)
	}
	if entries, err := os.ReadDir(f.tmp); err != nil || len(entries) != 0 {
		t.Errorf("temporary files remain after download failure: %v, %v", entries, err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
