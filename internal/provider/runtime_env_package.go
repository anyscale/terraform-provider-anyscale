package provider

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// Packaging for anyscale_runtime_env_package. The design and its source traces are in
// docs/decisions/service-working-dir-upload/README.md.
//
// The walk matches the Anyscale CLI's (utils/ray_utils.py: default excludes plus user
// excludes as one gitignore spec, every directory's .gitignore scoped to its own subtree,
// excluded directories pruned, empty directories kept, symlinks followed). The hash
// deliberately does NOT match the CLI's md5-of-zip-bytes: that one changes with mtimes and
// walk order, which in Terraform would be a redeploy on every fresh checkout.

const (
	// runtimeEnvPackageHashVersion prefixes the hash input, so a future change to the
	// encoding can never produce a colliding name for different content.
	runtimeEnvPackageHashVersion = "anyscale-runtime-env-package/v1\n"
	// runtimeEnvPackageZipRoot is the single top-level directory every entry sits under.
	// Ray strips one top-level directory from a remote zip (packaging.py, unzip_package with
	// remove_top_level_directory=True); without a wrapper, a source directory holding a single
	// subfolder would lose that folder. Constant, so the bytes do not depend on the source
	// directory's name.
	runtimeEnvPackageZipRoot = "pkg"
	// runtimeEnvPackageFileType is the cloud data bucket file type the CLI uses for packages.
	runtimeEnvPackageFileType = "runtime_env_packages"
	// runtimeEnvPackageLargeFile matches the CLI's per-file warning threshold.
	runtimeEnvPackageLargeFile = 100 << 20
	// runtimeEnvPackageLargeTotal is the total size above which the plan warns.
	runtimeEnvPackageLargeTotal = 500 << 20
	// runtimeEnvPackageUploadTimeout matches the presigned URL's lifetime.
	runtimeEnvPackageUploadTimeout = 30 * time.Minute
)

// runtimeEnvPackageDefaultExcludes are Ray's and the CLI's default excludes. The provider
// deliberately does not read RAY_OVERRIDE_RUNTIME_ENV_DEFAULT_EXCLUDES: an environment-dependent
// hash would differ between machines.
var runtimeEnvPackageDefaultExcludes = []string{".git", ".venv", "venv", "__pycache__"}

// runtimeEnvPackageZipTime is the fixed modification time of every zip entry (the zip format's
// earliest representable date).
var runtimeEnvPackageZipTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// runtimeEnvPackageFileName is the object name for a content hash.
func runtimeEnvPackageFileName(contentHash string) string {
	return "pkg_" + contentHash + ".zip"
}

// packageEntry is one included path: a regular file, or a directory that is empty on disk.
type packageEntry struct {
	rel  string // slash-separated, relative to the source directory
	abs  string
	dir  bool
	exec bool
	size int64
}

// packageTree is the result of walking a source directory.
type packageTree struct {
	entries    []packageEntry // sorted by rel
	largeFiles []string       // rel paths at or above runtimeEnvPackageLargeFile
	totalSize  int64
}

// walkPackageTree walks sourceDir and returns the entries that would be packaged.
func walkPackageTree(sourceDir string, excludes []string) (*packageTree, error) {
	info, err := os.Stat(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("source_dir %q: %w", sourceDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source_dir %q is not a directory", sourceDir)
	}

	patterns := make([]gitignore.Pattern, 0, len(runtimeEnvPackageDefaultExcludes)+len(excludes))
	for _, p := range runtimeEnvPackageDefaultExcludes {
		patterns = append(patterns, gitignore.ParsePattern(p, nil))
	}
	for _, p := range excludes {
		patterns = append(patterns, gitignore.ParsePattern(normalizeExcludePattern(p), nil))
	}

	tree := &packageTree{}
	w := &packageWalker{tree: tree}
	if err := w.walk(sourceDir, nil, []gitignore.Matcher{gitignore.NewMatcher(patterns)}, []os.FileInfo{info}); err != nil {
		return nil, err
	}
	if len(tree.entries) == 0 {
		return nil, fmt.Errorf("source_dir %q contains nothing to package after excludes and .gitignore rules", sourceDir)
	}
	sort.Slice(tree.entries, func(i, j int) bool { return tree.entries[i].rel < tree.entries[j].rel })
	return tree, nil
}

// normalizeExcludePattern cleans a typed path such as "./a/../b" the way the CLI's
// _normalize_exclude does, keeping a leading "!" and a trailing "/".
func normalizeExcludePattern(p string) string {
	negate := strings.HasPrefix(p, "!")
	body := strings.TrimPrefix(p, "!")
	if body == "" {
		return p
	}
	cleaned := path.Clean(body)
	if cleaned == "." {
		return p
	}
	if strings.HasSuffix(body, "/") && !strings.HasSuffix(cleaned, "/") {
		cleaned += "/"
	}
	if negate {
		return "!" + cleaned
	}
	return cleaned
}

type packageWalker struct {
	tree *packageTree
}

// walk visits dir. matchers are evaluated independently, as the CLI's list of excludes is:
// a path is excluded when ANY of them matches, so a .gitignore negation cannot re-include
// something a default or user exclude removed. ancestors detects symlink cycles.
func (w *packageWalker) walk(dir string, rel []string, matchers []gitignore.Matcher, ancestors []os.FileInfo) error {
	ps, err := readGitignorePatterns(filepath.Join(dir, ".gitignore"), rel)
	if err != nil {
		return err
	}
	if len(ps) > 0 {
		matchers = append(matchers[:len(matchers):len(matchers)], gitignore.NewMatcher(ps))
	}

	children, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read directory %q: %w", dir, err)
	}
	if len(children) == 0 && len(rel) > 0 {
		w.tree.entries = append(w.tree.entries, packageEntry{rel: strings.Join(rel, "/"), abs: dir, dir: true})
		return nil
	}

	for _, child := range children {
		abs := filepath.Join(dir, child.Name())
		parts := append(rel[:len(rel):len(rel)], child.Name())

		// os.Stat follows symlinks, as the CLI's Path.is_file/is_dir do. A broken symlink
		// is neither a file nor a directory to the CLI, so it is skipped here too.
		info, err := os.Stat(abs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat %q: %w", abs, err)
		}
		if excludedByAny(matchers, parts, info.IsDir()) {
			continue
		}

		switch {
		case info.IsDir():
			for _, a := range ancestors {
				if os.SameFile(a, info) {
					return fmt.Errorf("symlink cycle: %q leads back to one of its own parent directories", abs)
				}
			}
			if err := w.walk(abs, parts, matchers, append(ancestors[:len(ancestors):len(ancestors)], info)); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			relPath := strings.Join(parts, "/")
			w.tree.entries = append(w.tree.entries, packageEntry{
				rel:  relPath,
				abs:  abs,
				exec: info.Mode().Perm()&0o111 != 0,
				size: info.Size(),
			})
			w.tree.totalSize += info.Size()
			if info.Size() >= runtimeEnvPackageLargeFile {
				w.tree.largeFiles = append(w.tree.largeFiles, relPath)
			}
		default:
			// Sockets, devices and named pipes: the CLI skips them too.
		}
	}
	return nil
}

func excludedByAny(matchers []gitignore.Matcher, parts []string, isDir bool) bool {
	for _, m := range matchers {
		if m.Match(parts, isDir) {
			return true
		}
	}
	return false
}

// readGitignorePatterns parses dir/.gitignore, scoping each pattern to domain (the directory's
// path relative to the source directory). A missing file yields no patterns.
func readGitignorePatterns(file string, domain []string) ([]gitignore.Pattern, error) {
	f, err := os.Open(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		// A .gitignore that is a directory, or unreadable: the CLI only reads it when it
		// is a file.
		if info, statErr := os.Stat(file); statErr == nil && !info.Mode().IsRegular() {
			return nil, nil
		}
		return nil, fmt.Errorf("read %q: %w", file, err)
	}
	defer func() { _ = f.Close() }()

	var ps []gitignore.Pattern
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSuffix(s.Text(), "\r")
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		ps = append(ps, gitignore.ParsePattern(line, domain))
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("read %q: %w", file, err)
	}
	return ps, nil
}

// contentHash returns the tree's content hash without building a zip.
func (t *packageTree) contentHash() (string, error) {
	return t.writePackage(nil)
}

// writePackage streams every entry into zw (when non-nil) and returns the content hash of the
// bytes it read. Hashing the same bytes that go into the zip is what guarantees the uploaded
// object matches its name.
//
// The hash covers, per entry in sorted order: the slash-separated relative path, whether it is
// a file or an empty directory, the exec bit (Ray chmods from the zip's mode on extract, so it
// is semantic), and the sha256 of the file's bytes. It excludes mtimes, ownership, the absolute
// path and the source directory's name.
func (t *packageTree) writePackage(zw *zip.Writer) (string, error) {
	h := sha256.New()
	_, _ = io.WriteString(h, runtimeEnvPackageHashVersion)
	for _, e := range t.entries {
		kind, mode := byte('f'), fs.FileMode(0o644)
		if e.dir {
			kind, mode = 'd', fs.ModeDir|0o755
		} else if e.exec {
			mode = 0o755
		}
		execByte := byte('-')
		if e.exec {
			execByte = 'x'
		}
		var lenBuf [8]byte
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(e.rel)))
		_, _ = h.Write([]byte{kind, execByte})
		_, _ = h.Write(lenBuf[:])
		_, _ = io.WriteString(h, e.rel)

		var dst io.Writer
		if zw != nil {
			name := runtimeEnvPackageZipRoot + "/" + e.rel
			if e.dir {
				name += "/"
			}
			fh := &zip.FileHeader{Name: name, Method: zip.Store, Modified: runtimeEnvPackageZipTime}
			fh.SetMode(mode)
			w, err := zw.CreateHeader(fh)
			if err != nil {
				return "", fmt.Errorf("add %q to zip: %w", e.rel, err)
			}
			dst = w
		}
		if e.dir {
			continue
		}
		sum, err := hashFileInto(e.abs, dst)
		if err != nil {
			return "", err
		}
		_, _ = h.Write(sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashFileInto returns the sha256 of the file at p, copying its bytes to dst when non-nil.
func hashFileInto(p string, dst io.Writer) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	fh := sha256.New()
	w := io.Writer(fh)
	if dst != nil {
		w = io.MultiWriter(fh, dst)
	}
	if _, err := io.Copy(w, f); err != nil {
		return nil, fmt.Errorf("read %q: %w", p, err)
	}
	return fh.Sum(nil), nil
}

// buildPackageZip writes the zip for tree to a new temporary file and returns it with its
// content hash and size. The caller removes the file.
func buildPackageZip(tree *packageTree) (zipPath, contentHash string, size int64, err error) {
	f, err := os.CreateTemp("", "anyscale-runtime-env-package-*.zip")
	if err != nil {
		return "", "", 0, fmt.Errorf("create temporary zip: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("close temporary zip: %w", cerr)
		}
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()

	zw := zip.NewWriter(f)
	contentHash, err = tree.writePackage(zw)
	if err != nil {
		return "", "", 0, err
	}
	if err = zw.Close(); err != nil {
		return "", "", 0, fmt.Errorf("finish zip: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		return "", "", 0, fmt.Errorf("stat temporary zip: %w", err)
	}
	return f.Name(), contentHash, info.Size(), nil
}

// CloudDataBucketPresignedURL is the result of
// POST /api/v2/clouds/{cloud_id}/generate_cloud_data_bucket_presigned_url
// (CloudDataBucketPresignedUrlResponse). URL is a bearer credential for the object: never log
// it or put it in a diagnostic.
type CloudDataBucketPresignedURL struct {
	FileURI    string `json:"file_uri"`
	URL        string `json:"url"`
	FileExists bool   `json:"file_exists"`
	BucketName string `json:"bucket_name"`
	BucketPath string `json:"bucket_path"`
	URLScheme  string `json:"url_scheme"`
	FileSize   int64  `json:"file_size"`
}

type cloudDataBucketPresignedURLRequest struct {
	FileType        string  `json:"file_type"`
	FileName        string  `json:"file_name"`
	AccessMode      string  `json:"access_mode"`
	CloudResourceID *string `json:"cloud_resource_id,omitempty"`
}

// Access modes for RequestRuntimeEnvPackageURL.
const (
	CloudDataBucketRead  = "READ"
	CloudDataBucketWrite = "WRITE"
)

// presignedURLSchemeHTTP is the url_scheme for a URL used with a plain HTTP GET or PUT.
const presignedURLSchemeHTTP = "http"

// RequestRuntimeEnvPackageURL asks the control plane for a presigned URL for a runtime_env
// package object. cloudResourceID may be empty for the cloud's primary resource. The server
// probes the object on every call, so FileExists is live (except for url_scheme smart_open,
// where it is always false).
func RequestRuntimeEnvPackageURL(ctx context.Context, client *Client, cloudID, cloudResourceID, fileName, accessMode string) (*CloudDataBucketPresignedURL, error) {
	reqBody := cloudDataBucketPresignedURLRequest{
		FileType:   runtimeEnvPackageFileType,
		FileName:   fileName,
		AccessMode: accessMode,
	}
	if cloudResourceID != "" {
		reqBody.CloudResourceID = &cloudResourceID
	}
	body, err := MarshalRequestBody(reqBody)
	if err != nil {
		return nil, err
	}
	resp, err := DoRequestAndParse[struct {
		Result CloudDataBucketPresignedURL `json:"result"`
	}](ctx, client, http.MethodPost,
		fmt.Sprintf("/api/v2/clouds/%s/generate_cloud_data_bucket_presigned_url", url.PathEscape(cloudID)),
		body, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &resp.Result, nil
}

// checkPresignedUploadSupported rejects storage the provider cannot write to or has not been
// verified against. url_scheme smart_open means the CLI writes with the caller's own ambient
// cloud credentials; Azure needs extra blob headers and has not been tested.
func checkPresignedUploadSupported(p *CloudDataBucketPresignedURL) error {
	scheme, _, _ := strings.Cut(p.FileURI, "://")
	switch {
	case p.URLScheme != presignedURLSchemeHTTP:
		return fmt.Errorf("this cloud's storage returns url_scheme %q (an S3-compatible endpoint the Anyscale CLI writes with your own local credentials); anyscale_runtime_env_package supports only AWS S3 and Google Cloud Storage", p.URLScheme)
	case strings.HasPrefix(scheme, "azure") || scheme == "abfss":
		return fmt.Errorf("this cloud stores packages in Azure (%s://); anyscale_runtime_env_package does not support Azure storage yet", scheme)
	case scheme != "s3" && scheme != "gs":
		return fmt.Errorf("this cloud's storage URI scheme %q is not supported; anyscale_runtime_env_package supports only s3:// and gs://", scheme)
	}
	return nil
}

// uploadToPresignedURL PUTs size bytes of f to a presigned URL. It sends no Authorization header
// (the Anyscale token must never reach the bucket) and no Content-Type, matching what the CLI's
// requests.put signs against. Errors never contain the URL's query string.
func uploadToPresignedURL(ctx context.Context, httpClient *http.Client, presignedURL string, f *os.File, size int64) error {
	ctx, cancel := context.WithTimeout(ctx, runtimeEnvPackageUploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, io.NewSectionReader(f, 0, size))
	if err != nil {
		return fmt.Errorf("build upload request for %s: %s", redactPresignedURL(presignedURL), redactURLError(err))
	}
	// Without an explicit length, a file body is sent chunked, which presigned S3 PUTs reject.
	req.ContentLength = size
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(io.NewSectionReader(f, 0, size)), nil }

	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload to %s: %s", redactPresignedURL(presignedURL), redactURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("upload to %s: HTTP %d%s", redactPresignedURL(presignedURL), resp.StatusCode, storageErrorSummary(body))
	}
	return nil
}

// redactPresignedURL returns a presigned URL's scheme and host only. Its query string carries
// the signature, and its path is the object key, which the caller already reports.
func redactPresignedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "the presigned storage URL"
	}
	return u.Scheme + "://" + u.Host
}

// redactURLError strips the URL from a *url.Error, which embeds the full URL in its text.
func redactURLError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Op + ": " + ue.Err.Error()
	}
	return err.Error()
}

var (
	storageErrorCode    = regexp.MustCompile(`<Code>([^<]{1,200})</Code>`)
	storageErrorMessage = regexp.MustCompile(`<Message>([^<]{1,500})</Message>`)
)

// storageErrorSummary extracts only Code and Message from an S3 or GCS XML error body. The rest
// of such a body can echo the request signature (SignatureProvided, StringToSign).
func storageErrorSummary(body []byte) string {
	var parts []string
	if m := storageErrorCode.FindSubmatch(body); m != nil {
		parts = append(parts, string(m[1]))
	}
	if m := storageErrorMessage.FindSubmatch(body); m != nil {
		parts = append(parts, string(m[1]))
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, ": ")
}
