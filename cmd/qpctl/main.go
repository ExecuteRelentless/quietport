// qpctl: the operator's only administrative interface (FR-90). Runs on the operator's machine; circle keys never
// leave it except sealed to a member device or encrypted inside an export bundle.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	case "init":
		err = cmdInit(args)
	case "person":
		err = cmdPerson(args)
	case "circle":
		err = cmdCircle(args)
	case "invite":
		err = cmdInvite(args)
	case "device":
		err = cmdDevice(args)
	case "status":
		err = cmdStatus(args)
	case "logs":
		err = cmdLogs(args)
	case "offboard":
		err = cmdOffboard(args)
	case "backup":
		err = cmdBackup(args)
	case "keys":
		err = cmdKeys(args)
	case "audit":
		err = cmdAudit(args)
	case "settings":
		err = cmdSettings(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `qpctl `+Version+`

  qpctl init --hub http://100.64.0.1:8443 --token <operator token> [--proxy socks5://127.0.0.1:PORT] [--operator name]

  qpctl person add <name> --email <addr> [--household <name>]
  qpctl person list | show <name> | remove <name> --confirm <name>

  qpctl circle create <slug> --name "Display Name" [--quota 50G] [--mode bidirectional|send-only|receive-only] [--retention 30] [--exclude pat]...
  qpctl circle list | show <slug> | set <slug> [--quota 50G] [--mode m] [--retention d] [--bwlimit spec]
  qpctl circle add-member <slug> <person> [--readonly] | remove-member <slug> <person>
  qpctl circle rotate-key <slug> --confirm <slug> [--yes]
  qpctl circle destroy <slug> --confirm <slug>
  qpctl circle canary <slug>            (re)write the key-verification canary

  qpctl invite create <person> --circles a,b [--expires 24h]
  qpctl invite list | revoke <code-prefix>

  qpctl device list [--person <name>] | revoke <device-id> --confirm <device-id>

  qpctl status [<person>]           fleet health overview
  qpctl logs <person> [--tail 100]
  qpctl offboard <person> --confirm <person> [--yes]
  qpctl backup verify
  qpctl keys export --out FILE | split --out DIR [--shares 5] [--threshold 3] | recover --bundle FILE --share F... --out FILE | verify [--bundle FILE]
  qpctl audit [--limit 200]
`)
	os.Exit(2)
}

// --- config + keystore ---

func confDir() string {
	if x := os.Getenv("QPCTL_HOME"); x != "" {
		return x
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "quietport")
}

type Conf struct {
	Hub      string `json:"hub"`
	Proxy    string `json:"proxy,omitempty"`
	Operator string `json:"operator"`
	Host     string `json:"host,omitempty"`
	TokenSealed string `json:"token"`
}

func loadConf() (Conf, *Keystore, error) {
	var c Conf
	b, err := os.ReadFile(filepath.Join(confDir(), "qpctl.json"))
	if err != nil {
		return c, nil, errors.New("not initialised: run `qpctl init`")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, nil, err
	}
	ks, err := openKeystore()
	if err != nil {
		return c, nil, err
	}
	return c, ks, nil
}

func saveConf(c Conf) error {
	_ = os.MkdirAll(confDir(), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(confDir(), "qpctl.json"), b, 0o600)
}

func client() (*API, *Keystore, Conf, error) {
	c, ks, err := loadConf()
	if err != nil {
		return nil, nil, c, err
	}
	var tok string
	if err := ks.cred.OpenJSON(c.TokenSealed, &tok); err != nil {
		return nil, nil, c, errors.New("operator token unreadable; run `qpctl init` again")
	}
	api, err := NewAPI(c.Hub, tok, c.Proxy, c.Operator)
	return api, ks, c, err
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	hub := fs.String("hub", "", "hub API base, e.g. http://100.64.0.1:8443")
	tok := fs.String("token", "", "operator token from `qp-hub init-operator`")
	proxy := fs.String("proxy", "", "socks5://127.0.0.1:PORT (the local Quietport agent's proxy) or empty for a direct/ssh-tunnel path")
	op := fs.String("operator", "", "your name for the audit log")
	_ = fs.Parse(args)
	if *hub == "" || *tok == "" {
		return errors.New("--hub and --token are required")
	}
	ks, err := openKeystore()
	if err != nil {
		return err
	}
	sealed, err := ks.cred.SealJSON(*tok)
	if err != nil {
		return err
	}
	c := Conf{Hub: strings.TrimRight(*hub, "/"), Proxy: *proxy, Operator: *op, TokenSealed: sealed}
	if c.Operator == "" {
		c.Operator = os.Getenv("USER")
	}
	api, err := NewAPI(c.Hub, *tok, c.Proxy, c.Operator)
	if err != nil {
		return err
	}
	var ping struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
		Host    string `json:"host"`
	}
	if err := api.Get("/op/ping", &ping); err != nil {
		return fmt.Errorf("hub not reachable: %w", err)
	}
	c.Host = ping.Host
	if err := saveConf(c); err != nil {
		return err
	}
	fmt.Printf("connected to hub %s (qp-hub %s)\n", ping.Host, ping.Version)
	if ks.LastVerify.IsZero() {
		fmt.Println("note: run `qpctl keys export` and `qpctl keys verify` once your first circle exists.")
	}
	return nil
}

// --- persons ---

func cmdPerson(args []string) error {
	if len(args) < 1 {
		usage()
	}
	api, _, _, err := client()
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("person add", flag.ExitOnError)
		email := fs.String("email", "", "")
		hh := fs.String("household", "", "")
		name, rest := firstArg(args[1:])
		_ = fs.Parse(rest)
		if name == "" || *email == "" {
			return errors.New("usage: qpctl person add <name> --email <addr> [--household <name>]")
		}
		var p model.Person
		if err := api.Post("/op/persons", map[string]string{"name": name, "email": *email, "household": *hh}, &p); err != nil {
			return err
		}
		fmt.Printf("added %s (%s). Next: qpctl invite create %s --circles <slugs>\n", p.Name, p.Email, p.Name)
	case "list":
		var ps []model.Person
		if err := api.Get("/op/persons", &ps); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tEMAIL\tHOUSEHOLD\tSTATUS\tSINCE")
		for _, p := range ps {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Email, p.Household, p.Status, p.CreatedAt.Local().Format("2006-01-02"))
		}
		tw.Flush()
	case "show":
		name, _ := firstArg(args[1:])
		var v personView
		if err := api.Get("/op/persons/"+name, &v); err != nil {
			return err
		}
		printPerson(v)
	case "remove":
		fs := flag.NewFlagSet("person remove", flag.ExitOnError)
		confirm := fs.String("confirm", "", "")
		name, rest := firstArg(args[1:])
		_ = fs.Parse(rest)
		if err := needConfirm(*confirm, name); err != nil {
			return err
		}
		if err := api.Delete("/op/persons/" + name); err != nil {
			return err
		}
		fmt.Println("removed", name)
	default:
		usage()
	}
	return nil
}

type personView struct {
	model.Person
	Circles []model.Membership `json:"circles"`
	Devices []deviceView       `json:"devices"`
	Invites []model.Invite     `json:"invites"`
}
type deviceView struct {
	model.Device
	Heartbeat *model.Heartbeat `json:"heartbeat,omitempty"`
	Online    bool             `json:"online"`
}

func printPerson(v personView) {
	fmt.Printf("%s <%s> household=%s status=%s\n", v.Name, v.Email, v.Household, v.Status)
	for _, m := range v.Circles {
		fmt.Printf("  circle #%d %s\n", m.CircleID, m.Role)
	}
	for _, d := range v.Devices {
		printDevice(d)
	}
	for _, i := range v.Invites {
		state := "open"
		if i.ConsumedAt != nil {
			state = "used " + i.ConsumedAt.Local().Format("Jan 2 15:04")
		} else if time.Now().After(i.ExpiresAt) {
			state = "expired"
		}
		fmt.Printf("  invite %s… circles=%v %s\n", i.Prefix, i.CircleIDs, state)
	}
}

func printDevice(d deviceView) {
	on := "offline"
	if d.Online {
		on = "online"
	}
	fmt.Printf("  device #%d %s %s/%s v%s %s %s (%s) last heartbeat %s\n", d.ID, d.Hostname, d.OS, d.Arch, d.AgentVersion, d.TailnetIP, on, d.Status, agoStr(d.LastHeartbeat))
	if d.Heartbeat != nil {
		hb := d.Heartbeat
		fmt.Printf("      connection=%s free=%s errors=%d", hb.ConnectionType, human(hb.FreeDisk), hb.ErrorCount)
		if len(hb.Conditions) > 0 {
			fmt.Printf(" conditions=%v", hb.Conditions)
		}
		fmt.Println()
		keys := make([]string, 0, len(hb.PerCircle))
		for k := range hb.PerCircle {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			c := hb.PerCircle[k]
			e := ""
			if c.LastError != "" {
				e = " error: " + c.LastError
			}
			fmt.Printf("      %-16s last sync %s gen %d%s\n", k, agoStr(c.LastSync), c.Generation, e)
		}
	}
}

func agoStr(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func human(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || u == "TB" {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"TB", 1 << 40}, {"T", 1 << 40}, {"GB", 1 << 30}, {"G", 1 << 30}, {"MB", 1 << 20}, {"M", 1 << 20}, {"KB", 1 << 10}, {"K", 1 << 10}} {
		if strings.HasSuffix(s, u.suf) {
			mult = u.m
			s = strings.TrimSuffix(s, u.suf)
			break
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(f * float64(mult)), nil
}

func firstArg(args []string) (string, []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", args
	}
	return args[0], args[1:]
}

func needConfirm(got, want string) error {
	if want == "" {
		return errors.New("target required")
	}
	if got != want {
		return fmt.Errorf("this is destructive: repeat the target with --confirm %s", want) // FR-92
	}
	return nil
}

func ask(prompt string) bool {
	fmt.Print(prompt + " [y/N] ")
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "y")
}

var _ = cryptobox.NewToken
