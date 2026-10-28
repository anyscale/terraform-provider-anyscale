package provider

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeTree creates files under root. A key ending in "/" creates an empty directory; a value
// starting with "#!" is written executable.
func writeTree(t *testing.T, root string, files map[string]string, order []string) {
	t.Helper()
	if order == nil {
		for k := range files {
			order = append(order, k)
		}
	}
	for _, rel := range order {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(files[rel], "#!") {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(files[rel]), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
}

func treeHash(t *testing.T, root string, excludes ...string) string {
	t.Helper()
	tree, err := walkPackageTree(root, excludes)
	if err != nil {
		t.Fatalf("walkPackageTree: %v", err)
	}
	h, err := tree.contentHash()
	if err != nil {
		t.Fatalf("contentHash: %v", err)
	}
	return h
}

func treePaths(t *testing.T, root string, excludes ...string) []string {
	t.Helper()
	tree, err := walkPackageTree(root, excludes)
	if err != nil {
		t.Fatalf("walkPackageTree: %v", err)
	}
	var out []string
	for _, e := range tree.entries {
		p := e.rel
		if e.dir {
			p += "/"
		}
		out = append(out, p)
	}
	return out
}

var baseTree = map[string]string{
	"app.py":         "print('ok')\n",
	"a-b.txt":        "dash sorts before slash\n",
	"a/c.txt":        "nested\n",
	"bin/run.sh":     "#!/bin/sh\necho run\n",
	"empty/":         "",
	"deep/x/y/z.txt": "deep\n",
}

// The hash ignores everything that differs between a laptop and a fresh CI checkout of the
// same code: mtimes, creation order, absolute path, and the directory's own name.
func TestRuntimeEnvPackageHash_Invariant(t *testing.T) {
	a := filepath.Join(t.TempDir(), "one")
	b := filepath.Join(t.TempDir(), "somewhere", "else", "two")
	order := []string{"app.py", "a-b.txt", "a/c.txt", "bin/run.sh", "empty/", "deep/x/y/z.txt"}
	reversed := make([]string, len(order))
	for i, k := range order {
		reversed[len(order)-1-i] = k
	}
	writeTree(t, a, baseTree, order)
	writeTree(t, b, baseTree, reversed)

	want := treeHash(t, a)
	if got := treeHash(t, b); got != want {
		t.Fatalf("same content in a different place and order hashed differently: %s vs %s", got, want)
	}

	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	err := filepath.Walk(a, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := treeHash(t, a); got != want {
		t.Fatalf("changing only mtimes changed the hash: %s vs %s", got, want)
	}
}

func TestRuntimeEnvPackageHash_Sensitive(t *testing.T) {
	base := func(t *testing.T) string {
		d := t.TempDir()
		writeTree(t, d, baseTree, nil)
		return d
	}
	want := treeHash(t, base(t))

	cases := map[string]func(t *testing.T, d string){
		"content": func(t *testing.T, d string) {
			writeTree(t, d, map[string]string{"app.py": "print('changed')\n"}, nil)
		},
		"rename": func(t *testing.T, d string) {
			if err := os.Rename(filepath.Join(d, "app.py"), filepath.Join(d, "main.py")); err != nil {
				t.Fatal(err)
			}
		},
		"exec bit": func(t *testing.T, d string) {
			if runtime.GOOS == "windows" {
				t.Skip("no exec bit on Windows")
			}
			if err := os.Chmod(filepath.Join(d, "app.py"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"added empty dir": func(t *testing.T, d string) {
			if err := os.Mkdir(filepath.Join(d, "new"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"empty dir becomes file": func(t *testing.T, d string) {
			writeTree(t, d, map[string]string{"empty/f": ""}, nil)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := base(t)
			mutate(t, d)
			if got := treeHash(t, d); got == want {
				t.Fatalf("%s did not change the hash", name)
			}
		})
	}
}

func TestRuntimeEnvPackageWalk_Excludes(t *testing.T) {
	d := t.TempDir()
	writeTree(t, d, map[string]string{
		"app.py":                    "app",
		".git/HEAD":                 "ref",
		"venv/bin/python":           "py",
		"lib/__pycache__/m.pyc":     "pyc",
		"lib/.venv/x":               "x",
		"lib/m.py":                  "m",
		".gitignore":                "*.log\n# comment\n\n/top-only.txt\nbuild/\n",
		"root.log":                  "log",
		"top-only.txt":              "excluded at root",
		"sub/top-only.txt":          "kept: anchored pattern is root-only",
		"build/out.bin":             "out",
		"sub/.gitignore":            "*.tmp\n!keep.tmp\n",
		"sub/a.tmp":                 "tmp",
		"sub/keep.tmp":              "kept by negation",
		"sub/a.log":                 "log from parent gitignore",
		"other/b.tmp":               "kept: sub/.gitignore does not reach here",
		"data/big.csv":              "user exclude",
		"notes/secret.txt":          "user exclude via cleaned path",
		"notes/public.txt":          "kept",
		"neg/.gitignore":            "!*.csv\n",
		"neg/x.csv":                 "user exclude wins over a .gitignore negation",
		"sub/.gitignore-not-a-rule": "kept",
	}, nil)

	got := strings.Join(treePaths(t, d, "*.csv", "./notes/../notes/secret.txt"), "\n")
	want := strings.Join([]string{
		".gitignore",
		"app.py",
		"lib/m.py",
		"neg/.gitignore",
		"notes/public.txt",
		"other/b.tmp",
		"sub/.gitignore",
		"sub/.gitignore-not-a-rule",
		"sub/keep.tmp",
		"sub/top-only.txt",
	}, "\n")
	if got != want {
		t.Fatalf("included paths:\n%s\nwant:\n%s", got, want)
	}
}

// An excluded directory is never entered: a symlink cycle inside one is not an error.
func TestRuntimeEnvPackageWalk_PrunesExcludedDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	d := t.TempDir()
	writeTree(t, d, map[string]string{"app.py": "x", "skip/f": "y"}, nil)
	if err := os.Symlink("..", filepath.Join(d, "skip", "loop")); err != nil {
		t.Fatal(err)
	}
	if got := treePaths(t, d, "skip/"); strings.Join(got, ",") != "app.py" {
		t.Fatalf("got %v", got)
	}
}

func TestRuntimeEnvPackageWalk_Symlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"shared.py": "shared"}, nil)

	d := t.TempDir()
	writeTree(t, d, map[string]string{"app.py": "x"}, nil)
	if err := os.Symlink(filepath.Join(outside, "shared.py"), filepath.Join(d, "link.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(d, "missing"), filepath.Join(d, "broken")); err != nil {
		t.Fatal(err)
	}

	tree, err := walkPackageTree(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range tree.entries {
		paths = append(paths, e.rel)
	}
	if strings.Join(paths, ",") != "app.py,link.py" {
		t.Fatalf("got %v; want the symlinked file stored as content and the broken link skipped", paths)
	}

	// Same bytes as a regular file of the same name: the link is stored as content.
	plain := t.TempDir()
	writeTree(t, plain, map[string]string{"app.py": "x", "link.py": "shared"}, nil)
	if treeHash(t, d) != treeHash(t, plain) {
		t.Fatal("a symlinked file should hash as its target's content")
	}

	if err := os.Symlink(d, filepath.Join(d, "self")); err != nil {
		t.Fatal(err)
	}
	if _, err := walkPackageTree(d, nil); err == nil || !strings.Contains(err.Error(), "symlink cycle") {
		t.Fatalf("want a symlink cycle error, got %v", err)
	}
}

func TestRuntimeEnvPackageWalk_Errors(t *testing.T) {
	d := t.TempDir()
	if _, err := walkPackageTree(filepath.Join(d, "nope"), nil); err == nil {
		t.Fatal("want an error for a missing directory")
	}
	writeTree(t, d, map[string]string{"only.log": "x"}, nil)
	if _, err := walkPackageTree(filepath.Join(d, "only.log"), nil); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("want a not-a-directory error, got %v", err)
	}
	if _, err := walkPackageTree(d, []string{"*.log"}); err == nil || !strings.Contains(err.Error(), "nothing to package") {
		t.Fatalf("want an empty-package error, got %v", err)
	}
}

func TestNormalizeExcludePattern(t *testing.T) {
	for in, want := range map[string]string{
		"./a/../b":  "b",
		"a//b/":     "a/b/",
		"!./x":      "!x",
		".":         ".",
		"/abs/dir/": "/abs/dir/",
		"*.log":     "*.log",
	} {
		if got := normalizeExcludePattern(in); got != want {
			t.Errorf("normalizeExcludePattern(%q) = %q, want %q", in, got, want)
		}
	}
}

// runtimeEnvPackageGoldenZipSHA256 pins the exact zip bytes for baseTree. The object name comes
// from the content hash, not these bytes, so a change here alone never renames an object; but an
// unexplained change means the archive format moved and should be looked at.
const runtimeEnvPackageGoldenZipSHA256 = "e2a5a839f4ead3f5fc3201940f174068e1e215484d93d21c5778e25e99e56d17"

func TestRuntimeEnvPackageZip_Structure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no exec bit on Windows")
	}
	d := filepath.Join(t.TempDir(), "my-service")
	writeTree(t, d, baseTree, nil)
	tree, err := walkPackageTree(d, nil)
	if err != nil {
		t.Fatal(err)
	}
	zipPath, zipHash, size, err := buildPackageZip(tree)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(zipPath) }()

	if want, _ := tree.contentHash(); zipHash != want {
		t.Fatalf("hash of the zipped bytes %s differs from the planned hash %s", zipHash, want)
	}

	raw, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) != size {
		t.Fatalf("size %d, file has %d bytes", size, len(raw))
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != runtimeEnvPackageGoldenZipSHA256 {
		t.Errorf("zip bytes sha256 = %s, want %s", got, runtimeEnvPackageGoldenZipSHA256)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zr.Close() }()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if !strings.HasPrefix(f.Name, runtimeEnvPackageZipRoot+"/") {
			t.Errorf("entry %q is not under the single top-level directory %q", f.Name, runtimeEnvPackageZipRoot)
		}
		if f.Method != zip.Store {
			t.Errorf("entry %q method %d, want Store", f.Name, f.Method)
		}
		if !f.Modified.Equal(runtimeEnvPackageZipTime) {
			t.Errorf("entry %q modified %v, want %v", f.Name, f.Modified, runtimeEnvPackageZipTime)
		}
		// Ray chmods each extracted file from external_attr >> 16.
		mode := os.FileMode(f.ExternalAttrs>>16) & 0o777
		wantMode := os.FileMode(0o644)
		switch f.Name {
		case "pkg/bin/run.sh", "pkg/empty/":
			wantMode = 0o755
		}
		if mode != wantMode {
			t.Errorf("entry %q mode %o, want %o", f.Name, mode, wantMode)
		}
	}
	wantNames := "pkg/a-b.txt,pkg/a/c.txt,pkg/app.py,pkg/bin/run.sh,pkg/deep/x/y/z.txt,pkg/empty/"
	if strings.Join(names, ",") != wantNames {
		t.Fatalf("entries %v, want %s", names, wantNames)
	}
	rc, err := zr.File[2].Open()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(body) != baseTree["app.py"] {
		t.Fatalf("app.py content %q", body)
	}
}

func TestCheckPresignedUploadSupported(t *testing.T) {
	ok := []CloudDataBucketPresignedURL{
		{URLScheme: "http", FileURI: "s3://b/k.zip"},
		{URLScheme: "http", FileURI: "gs://b/k.zip"},
	}
	for _, p := range ok {
		if err := checkPresignedUploadSupported(&p); err != nil {
			t.Errorf("%s: unexpected error %v", p.FileURI, err)
		}
	}
	bad := []struct {
		info CloudDataBucketPresignedURL
		want string
	}{
		{CloudDataBucketPresignedURL{URLScheme: "smart_open", FileURI: "s3://b/k.zip"}, "smart_open"},
		{CloudDataBucketPresignedURL{URLScheme: "http", FileURI: "azure://c/k.zip"}, "Azure"},
		{CloudDataBucketPresignedURL{URLScheme: "http", FileURI: "abfss://c@a.dfs.core.windows.net/k.zip"}, "Azure"},
		{CloudDataBucketPresignedURL{URLScheme: "http", FileURI: "file:///tmp/k.zip"}, "not supported"},
		{CloudDataBucketPresignedURL{URLScheme: "http", FileURI: "no-scheme"}, "not supported"},
	}
	for _, c := range bad {
		err := checkPresignedUploadSupported(&c.info)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error mentioning %q", c.info.FileURI, err, c.want)
		}
	}
}

// Read must never act on file_exists from a smart_open response: it is hardcoded false there,
// so trusting it would recreate the resource on every plan.
func TestPackageObjectMissing(t *testing.T) {
	cases := []struct {
		info CloudDataBucketPresignedURL
		want bool
	}{
		{CloudDataBucketPresignedURL{URLScheme: "http", FileExists: false}, true},
		{CloudDataBucketPresignedURL{URLScheme: "http", FileExists: true}, false},
		{CloudDataBucketPresignedURL{URLScheme: "smart_open", FileExists: false}, false},
	}
	for _, c := range cases {
		if got := packageObjectMissing(&c.info); got != c.want {
			t.Errorf("%+v: got %v, want %v", c.info, got, c.want)
		}
	}
}

func TestUploadToPresignedURL(t *testing.T) {
	var gotAuth, gotType, gotTE string
	var gotLen int64
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotType, gotLen = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.ContentLength
		gotTE = strings.Join(r.TransferEncoding, ",")
		gotBody, _ = io.ReadAll(r.Body)
		if r.URL.Query().Get("fail") != "" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<Error><Code>SignatureDoesNotMatch</Code><Message>bad sig</Message><SignatureProvided>SECRETSIG</SignatureProvided></Error>`)
		}
	}))
	defer srv.Close()

	f, err := os.CreateTemp(t.TempDir(), "zip")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("zipbytes"); err != nil {
		t.Fatal(err)
	}

	if err := uploadToPresignedURL(context.Background(), srv.Client(), srv.URL+"/k.zip?X-Amz-Signature=SECRETSIG", f, 8); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" || gotType != "" || gotLen != 8 || gotTE != "" || string(gotBody) != "zipbytes" {
		t.Fatalf("auth=%q type=%q len=%d te=%q body=%q", gotAuth, gotType, gotLen, gotTE, gotBody)
	}

	err = uploadToPresignedURL(context.Background(), srv.Client(), srv.URL+"/k.zip?fail=1&X-Amz-Signature=SECRETSIG", f, 8)
	if err == nil || strings.Contains(err.Error(), "SECRETSIG") || !strings.Contains(err.Error(), "SignatureDoesNotMatch: bad sig") {
		t.Fatalf("got %v; want the storage error code without the signature", err)
	}

	err = uploadToPresignedURL(context.Background(), srv.Client(), "http://127.0.0.1:1/k.zip?X-Amz-Signature=SECRETSIG", f, 8)
	if err == nil || strings.Contains(err.Error(), "SECRETSIG") {
		t.Fatalf("got %v; want a transport error without the signature", err)
	}
}
