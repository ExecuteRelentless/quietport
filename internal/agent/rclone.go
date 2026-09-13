package agent

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"quietport.app/quietport/internal/model"
)

// Rclone wraps the bundled rclone for one circle. Remotes are configured through environment variables so that no
// key ever touches disk in plaintext (FR-16/48): an `s3` remote for the bucket and a `crypt` remote over it (FR-45/46).
type Rclone struct {
	bin   string
	proxy string
	logf  func(string, ...any)
}

var defaultExcludes = []string{"/" + VersionsDir + "/**", ".DS_Store", "Thumbs.db", "desktop.ini", "~$*", ".qp-*", "*.tmp.qp"} // FR-39

// cryptName is the name of the crypt remote over the circle's bucket. On Windows rclone reads a single letter before
// a colon as a drive letter, so "C:" there was the C drive: the agent's current directory, C:\Windows\System32 when
// Task Scheduler starts it (docs/adr/0013). Elsewhere it stays "C", the name bisync keys its listings by.
func cryptName(goos string) string {
	if goos == "windows" {
		return "QPCRYPT"
	}
	return "C"
}

var (
	cryptRemoteName = cryptName(runtime.GOOS)
	cryptRemote     = cryptRemoteName + ":"                                     // path prefix: "C:" or "QPCRYPT:"
	cryptEnvPrefix  = "RCLONE_CONFIG_" + strings.ToUpper(cryptRemoteName) + "_" // the prefix of its every setting
)

func (r *Rclone) env(cs CircleState, key model.CircleKey, ak, sk, endpoint string) []string {
	base := []string{}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "RCLONE_") || strings.HasPrefix(e, "HTTP_PROXY=") || strings.HasPrefix(e, "HTTPS_PROXY=") || strings.HasPrefix(e, "NO_PROXY=") {
			continue
		}
		base = append(base, e)
	}
	obs := func(s string) string {
		out, err := command(r.bin, "obscure", s).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return append(base,
		"HTTP_PROXY="+r.proxy, "HTTPS_PROXY="+r.proxy, "NO_PROXY=127.0.0.1,localhost", // FR-21
		"RCLONE_CONFIG_S3_TYPE=s3", "RCLONE_CONFIG_S3_PROVIDER=Other", "RCLONE_CONFIG_S3_ACCESS_KEY_ID="+ak, "RCLONE_CONFIG_S3_SECRET_ACCESS_KEY="+sk,
		"RCLONE_CONFIG_S3_ENDPOINT="+endpoint, "RCLONE_CONFIG_S3_REGION=garage", "RCLONE_CONFIG_S3_FORCE_PATH_STYLE=true",
		"RCLONE_CONFIG_S3_CHUNK_SIZE=64M", "RCLONE_CONFIG_S3_UPLOAD_CONCURRENCY=2", "RCLONE_CONFIG_S3_LEAVE_PARTS_ON_ERROR=true", // FR-36
		"RCLONE_CONFIG_S3_NO_CHECK_BUCKET=true",
		cryptEnvPrefix+"TYPE=crypt", cryptEnvPrefix+"REMOTE=s3:"+cs.Bucket+"/g"+strconv.Itoa(cs.Generation), cryptEnvPrefix+"FILENAME_ENCRYPTION=standard",
		cryptEnvPrefix+"DIRECTORY_NAME_ENCRYPTION=true", cryptEnvPrefix+"PASSWORD="+obs(key.Password), cryptEnvPrefix+"PASSWORD2="+obs(key.Salt),
		"RCLONE_CONFIG_DIR="+filepath.Join(AppDir(), "rclone-nocfg"), "RCLONE_CONFIG=/dev/null",
	)
}

// envMulti builds an environment with one S3 remote (S3:) and any number of crypt remotes over it, each pointing at
// its own generation prefix. Used for re-keying, where OLD: and NEW: are read and written in one rclone copy.
func (r *Rclone) envMulti(bucket, ak, sk, endpoint string, remotes map[string]model.CircleKey) []string {
	base := []string{}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "RCLONE_") || strings.HasPrefix(e, "HTTP_PROXY=") || strings.HasPrefix(e, "HTTPS_PROXY=") || strings.HasPrefix(e, "NO_PROXY=") {
			continue
		}
		base = append(base, e)
	}
	obs := func(s string) string {
		out, err := command(r.bin, "obscure", s).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	env := append(base,
		"HTTP_PROXY="+r.proxy, "HTTPS_PROXY="+r.proxy, "NO_PROXY=127.0.0.1,localhost",
		"RCLONE_CONFIG_S3_TYPE=s3", "RCLONE_CONFIG_S3_PROVIDER=Other", "RCLONE_CONFIG_S3_ACCESS_KEY_ID="+ak, "RCLONE_CONFIG_S3_SECRET_ACCESS_KEY="+sk,
		"RCLONE_CONFIG_S3_ENDPOINT="+endpoint, "RCLONE_CONFIG_S3_REGION=garage", "RCLONE_CONFIG_S3_FORCE_PATH_STYLE=true", "RCLONE_CONFIG_S3_NO_CHECK_BUCKET=true",
		"RCLONE_CONFIG_S3_CHUNK_SIZE=64M", "RCLONE_CONFIG_DIR="+filepath.Join(AppDir(), "rclone-nocfg"), "RCLONE_CONFIG=/dev/null")
	for name, k := range remotes {
		u := strings.ToUpper(name)
		env = append(env, "RCLONE_CONFIG_"+u+"_TYPE=crypt", "RCLONE_CONFIG_"+u+"_REMOTE=S3:"+bucket+"/g"+strconv.Itoa(k.Generation),
			"RCLONE_CONFIG_"+u+"_FILENAME_ENCRYPTION=standard", "RCLONE_CONFIG_"+u+"_DIRECTORY_NAME_ENCRYPTION=true",
			"RCLONE_CONFIG_"+u+"_PASSWORD="+obs(k.Password), "RCLONE_CONFIG_"+u+"_PASSWORD2="+obs(k.Salt))
	}
	return env
}

type Result struct {
	OK          bool
	Err         error
	Output      string
	NeedsResync bool
	Quota       bool
	PathTooLong bool
	Transferred int64
	Errors      int
	Duration    time.Duration
}

var (
	reTransferred = regexp.MustCompile(`Transferred:\s+([\d.]+)\s*([KMGT]?i?B)?\s*/`)
	reErrors      = regexp.MustCompile(`Errors:\s+(\d+)`)
	rePathLine    = regexp.MustCompile(`(ERROR|NOTICE|INFO)\s*:\s*(.+?):\s`)
)

// Sync runs one cycle for a circle in the mode that applies to this member (FR-7).
func (r *Rclone) Sync(ctx context.Context, cs CircleState, key model.CircleKey, ak, sk, endpoint string, mode string, resync bool) Result {
	local := CircleDir(cs.DisplayName)
	_ = os.MkdirAll(local, 0o755)
	stamp := time.Now().Format("2006-01-02T15-04-05")
	common := []string{"--stats", "0", "--log-level", "NOTICE", "--transfers", "2", "--checkers", "8", "--retries", "3", "--low-level-retries", "20",
		"--suffix", "." + stamp, "--suffix-keep-extension", "--use-json-log=false", "--fast-list=false", "--modify-window", "1s", "--color", "never"}
	for _, e := range defaultExcludes {
		common = append(common, "--exclude", e)
	}
	for _, e := range cs.Excludes {
		common = append(common, "--exclude", e)
	}
	for _, e := range cs.Excluded {
		common = append(common, "--exclude", e)
	}
	if cs.BwLimit != "" {
		common = append(common, "--bwlimit", cs.BwLimit) // FR-38 (timetable syntax works here)
	}
	var args []string
	switch mode {
	case "pull":
		args = append([]string{"sync", cryptRemote, local, "--backup-dir", filepath.Join(local, VersionsDir)}, common...)
	case "push":
		args = append([]string{"sync", local, cryptRemote, "--backup-dir", cryptRemote + VersionsDir}, common...)
	default: // bisync (FR-30/33)
		work := filepath.Join(BisyncDir(), cs.Slug)
		_ = os.MkdirAll(work, 0o700)
		args = append([]string{"bisync", local, cryptRemote, "--workdir", work, "--resilient", "--recover", "--max-lock", "2m",
			"--conflict-resolve", "newer", "--conflict-loser", "delete", "--conflict-suffix", "qpconflict",
			"--backup-dir1", filepath.Join(local, VersionsDir), "--backup-dir2", cryptRemote + VersionsDir,
			"--max-delete", "100", "--create-empty-src-dirs", "--compare", "size,modtime"}, common...)
		if resync {
			args = append(args, "--resync")
		}
	}
	start := time.Now()
	cmd := commandContext(ctx, r.bin, args...)
	cmd.Env = r.env(cs, key, ak, sk, endpoint)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	res := Result{OK: err == nil, Err: err, Output: out.String(), Duration: time.Since(start)}
	o := out.String()
	if m := reErrors.FindStringSubmatch(o); m != nil {
		res.Errors, _ = strconv.Atoi(m[1])
	}
	lo := strings.ToLower(o)
	res.Quota = strings.Contains(lo, "quota") // Garage: QuotaExceeded (FR-73)
	res.PathTooLong = strings.Contains(lo, "file name too long") || strings.Contains(lo, "name too long") || strings.Contains(lo, "path too long")
	// Only a state that bisync itself says is unusable earns a full resync; "retryable without --resync" errors
	// (empty prior listing after an empty first run, transient hub errors) are left to the next cycle (--resilient).
	res.NeedsResync = (strings.Contains(lo, "must run --resync") || strings.Contains(lo, "bisync critical error")) && !strings.Contains(lo, "retryable without --resync")
	if runtime.GOOS == "windows" {
		hideDir(filepath.Join(local, VersionsDir))
	}
	return res
}

// ScrubPaths removes plaintext file names from rclone output before it reaches the log (NFR-25): each path becomes a short hash.
func ScrubPaths(s string) string {
	return rePathLine.ReplaceAllStringFunc(s, func(m string) string {
		sub := rePathLine.FindStringSubmatch(m)
		h := sha1.Sum([]byte(sub[2]))
		return sub[1] + " : <" + hex.EncodeToString(h[:4]) + ">: "
	})
}

// PruneVersions deletes versions older than the retention period on both sides (FR-34).
func (r *Rclone) PruneVersions(ctx context.Context, cs CircleState, key model.CircleKey, ak, sk, endpoint string, days int) error {
	if days <= 0 {
		days = model.DefaultRetention
	}
	age := fmt.Sprintf("%dd", days)
	local := filepath.Join(CircleDir(cs.DisplayName), VersionsDir)
	for _, target := range []string{local, cryptRemote + VersionsDir} {
		cmd := commandContext(ctx, r.bin, "delete", target, "--min-age", age, "--rmdirs", "-q")
		cmd.Env = r.env(cs, key, ak, sk, endpoint)
		_ = cmd.Run() // a missing versions dir is fine
	}
	return nil
}

// RemoteTooLong lists remote files whose decrypted local path would exceed the platform limit (FR-40).
func (r *Rclone) RemoteTooLong(ctx context.Context, cs CircleState, key model.CircleKey, ak, sk, endpoint string) ([]string, error) {
	cmd := commandContext(ctx, r.bin, "lsf", "-R", "--files-only", cryptRemote)
	cmd.Env = r.env(cs, key, ak, sk, endpoint)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	base := CircleDir(cs.DisplayName)
	var bad []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		if len(filepath.Join(base, filepath.FromSlash(line))) > MaxPath() {
			bad = append(bad, "/"+line)
		}
	}
	return bad, nil
}

// Version of the bundled rclone.
func (r *Rclone) Version() string {
	out, err := command(r.bin, "version").Output()
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(strings.Split(string(out), "\n")[0])
}
