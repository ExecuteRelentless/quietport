package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/net/proxy"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func cmdCircle(args []string) error {
	if len(args) < 1 {
		usage()
	}
	api, ks, conf, err := client()
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("circle create", flag.ExitOnError)
		name := fs.String("name", "", "display name (becomes the folder name)")
		quota := fs.String("quota", "0", "e.g. 50G (0 = unlimited)")
		mode := fs.String("mode", model.ModeBidirectional, "bidirectional | send-only | receive-only")
		ret := fs.Int("retention", model.DefaultRetention, "days to keep old versions")
		bw := fs.String("bwlimit", "", "rclone bwlimit, e.g. 2M or a timetable \"08:00,1M 22:00,off\"")
		inv := fs.String("invites", model.InviteByMembers, "who may create invite links: members (any read/write member, from their folder) | operator")
		var ex stringList
		fs.Var(&ex, "exclude", "exclusion pattern (repeatable)")
		slug, rest := firstArg(args[1:])
		_ = fs.Parse(rest)
		if slug == "" || *name == "" {
			return errors.New("usage: qpctl circle create <slug> --name \"Display Name\" [--quota 50G] [--mode bidirectional] [--invites members|operator]")
		}
		q, err := parseSize(*quota)
		if err != nil {
			return err
		}
		var c model.Circle
		if err := api.Post("/op/circles", map[string]any{"slug": slug, "name": *name, "quota": q, "mode": *mode, "retention": *ret, "excludes": []string(ex), "bwlimit": *bw, "invites": *inv}, &c); err != nil {
			return err
		}
		// FR-49: the key is born here, on the operator's machine, and goes nowhere in plaintext
		key := model.CircleKey{Slug: slug, Generation: 1, Password: cryptobox.NewCircleSecret(), Salt: cryptobox.NewCircleSecret()}
		ks.Add(key)
		if err := ks.save(); err != nil {
			return err
		}
		fmt.Printf("created circle %s (%q) generation 1\n", c.Slug, c.DisplayName)
		if err := writeCanary(api, conf, c, key); err != nil {
			fmt.Println("warning: canary object not written:", err)
		} else {
			fmt.Println("canary written; `qpctl keys verify` can now prove a backup of this key.")
		}
		if ks.LastExport.IsZero() || time.Since(ks.LastExport) > 24*time.Hour {
			fmt.Println("reminder: export your keys (qpctl keys export / keys split). Loss of all copies is unrecoverable by design.")
		}
	case "list":
		var cs []model.Circle
		if err := api.Get("/op/circles", &cs); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SLUG\tNAME\tMODE\tGEN\tUSED\tQUOTA\tMEMBERS\tKEY")
		for _, c := range cs {
			_, have := ks.Gen(c.Slug, c.Generation)
			k := "missing!"
			if have {
				k = "ok"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%d\t%s\n", c.Slug, c.DisplayName, c.SyncMode, c.Generation, human(c.UsedBytes), quotaStr(c.QuotaBytes), len(c.Members), k)
		}
		tw.Flush()
	case "show":
		slug, _ := firstArg(args[1:])
		var c model.Circle
		if err := api.Get("/op/circles/"+slug, &c); err != nil {
			return err
		}
		fmt.Printf("%s %q mode=%s generation=%d used=%s quota=%s retention=%dd invites=%s bucket=%s\n", c.Slug, c.DisplayName, c.SyncMode, c.Generation, human(c.UsedBytes), quotaStr(c.QuotaBytes), c.VersionRetentionDays, c.InvitePolicy, c.BucketPrefix)
		if len(c.Excludes) > 0 {
			fmt.Printf("  excludes: %v\n", c.Excludes)
		}
		if c.BwLimit != "" {
			fmt.Printf("  bwlimit: %s\n", c.BwLimit)
		}
		for _, m := range c.Members {
			fmt.Printf("  member %s (%s) since %s\n", m.PersonName, m.Role, m.AddedAt.Local().Format("2006-01-02"))
		}
		if _, ok := ks.Gen(c.Slug, c.Generation); !ok {
			fmt.Println("  WARNING: this machine's keystore has no key for the current generation")
		}
	case "set":
		fs := flag.NewFlagSet("circle set", flag.ExitOnError)
		quota := fs.String("quota", "", "")
		mode := fs.String("mode", "", "")
		ret := fs.Int("retention", 0, "")
		bw := fs.String("bwlimit", "\x00", "")
		inv := fs.String("invites", "", "members | operator")
		name := fs.String("name", "", "")
		var ex stringList
		fs.Var(&ex, "exclude", "")
		slug, rest := firstArg(args[1:])
		_ = fs.Parse(rest)
		patch := map[string]any{}
		if *quota != "" {
			q, err := parseSize(*quota)
			if err != nil {
				return err
			}
			patch["quota"] = q
		}
		if *mode != "" {
			patch["mode"] = *mode
		}
		if *ret > 0 {
			patch["retention"] = *ret
		}
		if *bw != "\x00" {
			patch["bwlimit"] = *bw
		}
		if *name != "" {
			patch["name"] = *name
		}
		if *inv != "" {
			patch["invites"] = *inv
		}
		if len(ex) > 0 {
			patch["excludes"] = []string(ex)
		}
		var c model.Circle
		if err := api.Patch("/op/circles/"+slug, patch, &c); err != nil {
			return err
		}
		fmt.Println("updated", c.Slug)
	case "add-member":
		fs := flag.NewFlagSet("add-member", flag.ExitOnError)
		ro := fs.Bool("readonly", false, "")
		slug, rest := firstArg(args[1:])
		person, rest := firstArg(rest)
		_ = fs.Parse(rest)
		if slug == "" || person == "" {
			return errors.New("usage: qpctl circle add-member <slug> <person> [--readonly]")
		}
		role := "member"
		if *ro {
			role = "readonly"
		}
		var out struct {
			Devices    []model.Device `json:"devices"`
			Generation int            `json:"generation"`
			CircleID   int64          `json:"circle_id"`
		}
		if err := api.Post("/op/circles/"+slug+"/members", map[string]string{"person": person, "role": role}, &out); err != nil {
			return err
		}
		n, err := grantToDevices(api, ks, slug, out.CircleID, out.Generation, out.Devices)
		if err != nil {
			return err
		}
		fmt.Printf("%s added to %s; key delivered to %d device(s) on their next heartbeat (within 5 minutes)\n", person, slug, n) // FR-111
	case "remove-member":
		slug, rest := firstArg(args[1:])
		person, _ := firstArg(rest)
		if err := api.Delete("/op/circles/" + slug + "/members/" + person); err != nil {
			return err
		}
		fmt.Printf("%s removed from %s. Their copy keeps what it already had; rotate the key to lock them out of new content: qpctl circle rotate-key %s --confirm %s\n", person, slug, slug, slug)
	case "rotate-key":
		fs := flag.NewFlagSet("rotate-key", flag.ExitOnError)
		confirm := fs.String("confirm", "", "")
		yes := fs.Bool("yes", false, "skip the long-rotation prompt")
		slug, rest := firstArg(args[1:])
		_ = fs.Parse(rest)
		if err := needConfirm(*confirm, slug); err != nil {
			return err
		}
		_, err := rotateKey(api, ks, conf, slug, *yes)
		return err
	case "canary":
		slug, _ := firstArg(args[1:])
		var c model.Circle
		if err := api.Get("/op/circles/"+slug, &c); err != nil {
			return err
		}
		key, ok := ks.Gen(slug, c.Generation)
		if !ok {
			return fmt.Errorf("keystore has no key for %s generation %d", slug, c.Generation)
		}
		if err := writeCanary(api, conf, c, key); err != nil {
			return err
		}
		fmt.Println("canary written for", slug)
	case "destroy":
		fs := flag.NewFlagSet("destroy", flag.ExitOnError)
		confirm := fs.String("confirm", "", "")
		slug, rest := firstArg(args[1:])
		_ = fs.Parse(rest)
		if err := needConfirm(*confirm, slug); err != nil {
			return err
		}
		if err := api.Delete("/op/circles/" + slug); err != nil {
			return err
		}
		fmt.Printf("destroyed %s on the hub. Keys stay in this keystore in case a backup ever needs decrypting.\n", slug)
	default:
		usage()
	}
	return nil
}

func quotaStr(n int64) string {
	if n == 0 {
		return "unlimited"
	}
	return human(n)
}

// grantToDevices seals the current key of slug to every active device (FR-91/111) and uploads the sealed boxes.
func grantToDevices(api *API, ks *Keystore, slug string, circleID int64, gen int, devices []model.Device) (int, error) {
	key, ok := ks.Gen(slug, gen)
	if !ok {
		return 0, fmt.Errorf("keystore has no key for %s generation %d", slug, gen)
	}
	var grants []model.KeyGrant
	for _, d := range devices {
		if d.Status == model.StatusRevoked || d.PubKey == "" {
			continue
		}
		box, err := cryptobox.SealToDevice(d.PubKey, key)
		if err != nil {
			return 0, err
		}
		grants = append(grants, model.KeyGrant{DeviceID: d.ID, CircleID: circleID, Generation: gen, SealedBox: box})
	}
	if len(grants) == 0 {
		return 0, nil
	}
	return len(grants), api.Post("/op/grants", grants, nil)
}

// --- rclone on the operator's machine (re-encryption, canary) ---

func rcloneBin() (string, error) {
	if x := os.Getenv("QPCTL_RCLONE"); x != "" {
		return x, nil
	}
	self, _ := os.Executable()
	for _, p := range []string{filepath.Join(filepath.Dir(self), "rclone"), filepath.Join(filepath.Dir(self), "rclone.exe")} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath("rclone"); err == nil {
		return p, nil
	}
	return "", errors.New("rclone not found: put it next to qpctl, on PATH, or set QPCTL_RCLONE")
}

type opKey struct {
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	Endpoint   string `json:"endpoint"`
	Bucket     string `json:"bucket"`
	UsedBytes  int64  `json:"used_bytes"`
	Generation int    `json:"generation"`
}

func rcloneEnv(conf Conf, k opKey, remotes map[string]model.CircleKey) ([]string, error) {
	bin, err := rcloneBin()
	if err != nil {
		return nil, err
	}
	obs := func(s string) string {
		out, _ := exec.Command(bin, "obscure", s).Output()
		return strings.TrimSpace(string(out))
	}
	env := []string{}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "RCLONE_") || strings.HasPrefix(e, "HTTP_PROXY=") || strings.HasPrefix(e, "HTTPS_PROXY=") {
			continue
		}
		env = append(env, e)
	}
	if conf.Proxy != "" {
		env = append(env, "HTTP_PROXY="+conf.Proxy, "HTTPS_PROXY="+conf.Proxy, "NO_PROXY=127.0.0.1,localhost")
	}
	env = append(env, "RCLONE_CONFIG=/dev/null", "RCLONE_CONFIG_S3_TYPE=s3", "RCLONE_CONFIG_S3_PROVIDER=Other", "RCLONE_CONFIG_S3_ACCESS_KEY_ID="+k.AccessKey,
		"RCLONE_CONFIG_S3_SECRET_ACCESS_KEY="+k.SecretKey, "RCLONE_CONFIG_S3_ENDPOINT="+k.Endpoint, "RCLONE_CONFIG_S3_REGION=garage", "RCLONE_CONFIG_S3_FORCE_PATH_STYLE=true",
		"RCLONE_CONFIG_S3_NO_CHECK_BUCKET=true", "RCLONE_CONFIG_S3_CHUNK_SIZE=64M")
	for name, ck := range remotes {
		u := strings.ToUpper(name)
		env = append(env, "RCLONE_CONFIG_"+u+"_TYPE=crypt", "RCLONE_CONFIG_"+u+"_REMOTE=s3:"+k.Bucket+"/g"+strconv.Itoa(ck.Generation),
			"RCLONE_CONFIG_"+u+"_PASSWORD="+obs(ck.Password), "RCLONE_CONFIG_"+u+"_PASSWORD2="+obs(ck.Salt))
	}
	return env, nil
}

func runRclone(env []string, args ...string) (string, error) {
	bin, err := rcloneBin()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, append(args, "--contimeout", "15s", "--timeout", "60s", "--low-level-retries", "3")...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("rclone %s: %v: %s", args[0], err, lastLines(string(out), 3))
	}
	return string(out), nil
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " | ")
}

const canaryName = ".qp-canary"

// storageReachable: the hub's object store lives on the tailnet, so this machine must be an enrolled member
// (proxy configured) or on an ssh tunnel that forwards it. A quick dial avoids minutes of rclone retries.
func storageReachable(conf Conf, endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	var d proxy.Dialer = &net.Dialer{Timeout: 4 * time.Second}
	if conf.Proxy != "" {
		pu, err := url.Parse(conf.Proxy)
		if err != nil {
			return false
		}
		d, err = proxy.FromURL(pu, &net.Dialer{Timeout: 4 * time.Second})
		if err != nil {
			return false
		}
	}
	c, err := d.Dial("tcp", u.Host)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

var errStorageUnreachable = errors.New("hub storage is not reachable from this machine: enrol this machine into a circle (qpctl invite create <you> …) and set --proxy to its local port in `qpctl init`, or forward the storage port over ssh")

func canaryText(slug string) string { return "quietport canary " + slug + "\n" }

func writeCanary(api *API, conf Conf, c model.Circle, key model.CircleKey) error {
	var k opKey
	if err := api.Post("/op/circles/"+c.Slug+"/opkey", nil, &k); err != nil {
		return err
	}
	defer api.Delete("/op/circles/" + c.Slug + "/opkey/" + k.AccessKey)
	if !storageReachable(conf, k.Endpoint) {
		return errStorageUnreachable
	}
	env, err := rcloneEnv(conf, k, map[string]model.CircleKey{"c": key})
	if err != nil {
		return err
	}
	tmp, _ := os.CreateTemp("", "qp-canary-*")
	_, _ = tmp.WriteString(canaryText(c.Slug))
	tmp.Close()
	defer os.Remove(tmp.Name())
	_, err = runRclone(env, "copyto", tmp.Name(), "c:"+canaryName, "-q")
	return err
}

// rotateKey: FR-91 / FR-115 steps 3-5 / FR-117. Runs the re-encryption through this machine, because only this
// machine and member devices hold keys.
func rotateKey(api *API, ks *Keystore, conf Conf, slug string, yes bool) (time.Duration, error) {
	var c model.Circle
	if err := api.Get("/op/circles/"+slug, &c); err != nil {
		return 0, err
	}
	oldKey, ok := ks.Gen(slug, c.Generation)
	if !ok {
		return 0, fmt.Errorf("keystore has no key for %s generation %d; cannot re-encrypt", slug, c.Generation)
	}
	var k opKey
	if err := api.Post("/op/circles/"+slug+"/opkey", nil, &k); err != nil {
		return 0, err
	}
	defer api.Delete("/op/circles/" + slug + "/opkey/" + k.AccessKey)
	if !storageReachable(conf, k.Endpoint) {
		return 0, errStorageUnreachable
	}
	// estimate (FR-117): data must come down and go back up through this machine
	const assumedBytesPerSec = 4 << 20
	est := time.Duration(float64(k.UsedBytes*2)/assumedBytesPerSec) * time.Second
	fmt.Printf("%s holds %s; re-encryption through this machine is estimated at %s\n", slug, human(k.UsedBytes), est.Round(time.Second))
	if est > 10*time.Minute && !yes {
		if !ask("Continue?") {
			return 0, errors.New("cancelled")
		}
	}
	newKey := model.CircleKey{Slug: slug, Generation: c.Generation + 1, Password: cryptobox.NewCircleSecret(), Salt: cryptobox.NewCircleSecret()}
	ks.Add(newKey)
	if err := ks.save(); err != nil {
		return 0, err
	}
	start := time.Now()
	env, err := rcloneEnv(conf, k, map[string]model.CircleKey{"old": oldKey, "new": newKey})
	if err != nil {
		return 0, err
	}
	fmt.Println("re-encrypting…")
	if _, err := runRclone(env, "copy", "old:", "new:", "--transfers", "4", "--checkers", "8", "-q", "--retries", "5"); err != nil {
		return 0, fmt.Errorf("re-encryption failed, generation not switched: %w", err)
	}
	// grants for the new generation go up BEFORE the switch so members see key and generation together
	var members []model.Device
	if err := api.Get("/op/devices", &members); err != nil {
		return 0, err
	}
	memberIDs := map[int64]bool{}
	for _, m := range c.Members {
		memberIDs[m.PersonID] = true
	}
	var eligible []model.Device
	for _, d := range members {
		if memberIDs[d.PersonID] && d.Status == model.StatusActive {
			eligible = append(eligible, d)
		}
	}
	n, err := grantToDevices(api, ks, slug, c.ID, newKey.Generation, eligible)
	if err != nil {
		return 0, err
	}
	if err := api.Post("/op/circles/"+slug+"/generation", map[string]int{"generation": newKey.Generation}, nil); err != nil {
		return 0, err
	}
	if _, err := runRclone(env, "purge", "s3:"+k.Bucket+"/g"+strconv.Itoa(c.Generation), "-q"); err != nil {
		fmt.Println("warning: old ciphertext not fully removed:", err)
	}
	took := time.Since(start)
	fmt.Printf("rotated %s to generation %d in %s; new key delivered to %d device(s)\n", slug, newKey.Generation, took.Round(time.Second), n)
	return took, nil
}
