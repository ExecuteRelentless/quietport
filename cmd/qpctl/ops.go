package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

func cmdInvite(args []string) error {
	if len(args) < 1 {
		usage()
	}
	api, ks, conf, err := client()
	if err != nil {
		return err
	}
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("invite create", flag.ExitOnError)
		circles := fs.String("circles", "", "comma-separated slugs")
		exp := fs.String("expires", "24h", "")
		person, rest := firstArg(args[1:])
		_ = fs.Parse(rest)
		if person == "" || *circles == "" {
			return errors.New("usage: qpctl invite create <person> --circles family,photos [--expires 24h]")
		}
		var keys []model.CircleKey
		var ids []int64
		for _, slug := range strings.Split(*circles, ",") {
			slug = strings.TrimSpace(slug)
			var c model.Circle
			if err := api.Get("/op/circles/"+slug, &c); err != nil {
				return fmt.Errorf("circle %s: %w", slug, err)
			}
			k, ok := ks.Gen(slug, c.Generation)
			if !ok {
				return fmt.Errorf("keystore has no key for %s generation %d", slug, c.Generation)
			}
			keys = append(keys, k) // FR-13: only the circles named here
			ids = append(ids, c.ID)
		}
		code := cryptobox.NewInviteCode()
		sealed, err := cryptobox.SealWithCode(code, keys)
		if err != nil {
			return err
		}
		var out struct {
			ExpiresAt time.Time `json:"expires_at"`
			URLBase   string    `json:"url_base"`
		}
		if err := api.Post("/op/invites", map[string]any{"person": person, "circle_ids": ids, "ttl": *exp, "sealed_keys": sealed, "code_hash": cryptobox.HashToken(code), "prefix": code[:6]}, &out); err != nil {
			return err
		}
		fmt.Printf("%s%s\n", out.URLBase, code)
		fmt.Printf("send this link to %s; it works once and expires %s\n", person, out.ExpiresAt.Local().Format("Mon Jan 2 15:04"))
	case "list":
		var invs []model.Invite
		if err := api.Get("/op/invites", &invs); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "CODE\tPERSON\tCIRCLES\tCREATED\tSTATE")
		for _, i := range invs {
			state := "open, expires " + i.ExpiresAt.Local().Format("Jan 2 15:04")
			if i.ConsumedAt != nil {
				state = "used " + i.ConsumedAt.Local().Format("Jan 2 15:04")
			} else if time.Now().After(i.ExpiresAt) {
				state = "expired"
			}
			fmt.Fprintf(tw, "%s…\t%s\t%v\t%s\t%s\n", i.Prefix, i.PersonName, i.CircleIDs, i.CreatedAt.Local().Format("Jan 2 15:04"), state)
		}
		tw.Flush()
	case "revoke":
		prefix, _ := firstArg(args[1:])
		if len(prefix) < 6 {
			return errors.New("give the first 6+ characters of the code")
		}
		if err := api.Delete("/op/invites/" + prefix[:6]); err != nil {
			return err
		}
		fmt.Println("revoked", prefix[:6]+"…")
	default:
		usage()
	}
	_ = conf
	return nil
}

func cmdDevice(args []string) error {
	if len(args) < 1 {
		usage()
	}
	api, _, _, err := client()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("device list", flag.ExitOnError)
		person := fs.String("person", "", "")
		_ = fs.Parse(args[1:])
		var devs []deviceView
		q := ""
		if *person != "" {
			q = "?person=" + *person
		}
		if err := api.Get("/op/devices"+q, &devs); err != nil {
			return err
		}
		for _, d := range devs {
			printDevice(d)
		}
		if len(devs) == 0 {
			fmt.Println("no devices")
		}
	case "revoke":
		fs := flag.NewFlagSet("device revoke", flag.ExitOnError)
		confirm := fs.String("confirm", "", "")
		id, rest := firstArg(args[1:])
		_ = fs.Parse(rest)
		if err := needConfirm(*confirm, id); err != nil {
			return err
		}
		var out struct {
			Circles []string `json:"circles"`
		}
		if err := api.DeleteOut("/op/devices/"+id, &out); err != nil {
			return err
		}
		fmt.Printf("device %s revoked from the mesh. It keeps the keys it already had: rotate %v to lock it out of new content.\n", id, out.Circles) // §9 stolen device
	default:
		usage()
	}
	return nil
}

type statusView struct {
	Hub     map[string]any `json:"hub"`
	Persons []personView   `json:"persons"`
	Circles []model.Circle `json:"circles"`
	Backup  map[string]any `json:"backup"`
}

func cmdStatus(args []string) error {
	api, ks, _, err := client()
	if err != nil {
		return err
	}
	if name, _ := firstArg(args); name != "" {
		var v personView
		if err := api.Get("/op/persons/"+name, &v); err != nil {
			return err
		}
		printPerson(v)
		return nil
	}
	var sv statusView
	if err := api.Get("/op/status", &sv); err != nil {
		return err
	}
	fmt.Printf("hub %s  qp-hub %v  tailnet %v\n", sv.Hub["host"], sv.Hub["version"], sv.Hub["tailnet_ip"])
	if b, ok := sv.Backup["last"].(map[string]any); ok && b != nil {
		fmt.Printf("backup: last %v (%v)  ", b["finished"], b["result"])
	} else {
		fmt.Print("backup: never run  ")
	}
	if c, ok := sv.Backup["canary"].(map[string]any); ok && c != nil {
		fmt.Printf("canary: %v at %v\n", c["result"], c["checked"])
	} else {
		fmt.Println("canary: never checked")
	}
	if ks.LastVerify.IsZero() || time.Since(ks.LastVerify) > 365*24*time.Hour {
		fmt.Println("KEYS: `qpctl keys verify` has not been run in the last 365 days (FR-123)")
	}
	fmt.Println()
	fmt.Println("CIRCLES")
	for _, c := range sv.Circles {
		fmt.Printf("  %-14s %-18s gen %d  %s / %s  %d members\n", c.Slug, c.DisplayName, c.Generation, human(c.UsedBytes), quotaStr(c.QuotaBytes), len(c.Members))
	}
	fmt.Println()
	fmt.Println("PEOPLE")
	for _, p := range sv.Persons {
		online := 0
		for _, d := range p.Devices {
			if d.Online {
				online++
			}
		}
		flags := ""
		for _, d := range p.Devices {
			if d.Heartbeat != nil && len(d.Heartbeat.Conditions) > 0 {
				flags += " " + strings.Join(d.Heartbeat.Conditions, ",")
			}
			if d.Heartbeat != nil && d.Heartbeat.ConnectionType == "relayed" {
				flags += " relayed"
			}
			if d.Status != model.StatusActive {
				flags += " " + d.Status
			}
		}
		fmt.Printf("  %-12s %-8s %d device(s), %d online, %d circle(s)%s\n", p.Name, p.Status, len(p.Devices), online, len(p.Circles), flags)
	}
	return nil
}

func cmdLogs(args []string) error {
	api, _, _, err := client()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	tail := fs.Int("tail", 100, "")
	name, rest := firstArg(args)
	_ = fs.Parse(rest)
	if name == "" {
		return errors.New("usage: qpctl logs <person> [--tail 100]")
	}
	var out map[string]json.RawMessage
	if err := api.Get("/op/logs/"+name+"?tail="+strconv.Itoa(*tail), &out); err != nil {
		return err
	}
	for k, v := range out {
		if k == "events" {
			var evs []model.Event
			_ = json.Unmarshal(v, &evs)
			fmt.Println("EVENTS")
			for _, e := range evs {
				fmt.Printf("  %s %s %s\n", e.Timestamp.Local().Format("Jan 2 15:04"), e.Kind, e.Detail)
			}
			continue
		}
		var hbs []model.Heartbeat
		_ = json.Unmarshal(v, &hbs)
		fmt.Printf("DEVICE %s (%d heartbeats)\n", k, len(hbs))
		for i, hb := range hbs {
			if i >= *tail {
				break
			}
			fmt.Printf("  %s v%s %s errors=%d free=%s conds=%v\n", hb.Timestamp.Local().Format("Jan 2 15:04"), hb.AgentVersion, hb.ConnectionType, hb.ErrorCount, human(hb.FreeDisk), hb.Conditions)
		}
	}
	return nil
}

// cmdOffboard: FR-115/116/117. Hub revokes devices and memberships; this machine rotates and re-encrypts.
func cmdOffboard(args []string) error {
	api, ks, conf, err := client()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("offboard", flag.ExitOnError)
	confirm := fs.String("confirm", "", "")
	yes := fs.Bool("yes", false, "")
	name, rest := firstArg(args)
	_ = fs.Parse(rest)
	if err := needConfirm(*confirm, name); err != nil {
		return err
	}
	start := time.Now()
	var out struct {
		DevicesRevoked []int64  `json:"devices_revoked"`
		Circles        []string `json:"circles"`
	}
	if err := api.Post("/op/persons/"+name+"/offboard", nil, &out); err != nil {
		return err
	}
	fmt.Printf("1-2. revoked %d device(s) and removed %s from %d circle(s)\n", len(out.DevicesRevoked), name, len(out.Circles))
	var rotated []string
	var total time.Duration
	for _, slug := range out.Circles {
		took, err := rotateKey(api, ks, conf, slug, *yes)
		if err != nil {
			fmt.Printf("   %s: rotation FAILED: %v (re-run: qpctl circle rotate-key %s --confirm %s)\n", slug, err, slug, slug)
			continue
		}
		rotated = append(rotated, slug)
		total += took
	}
	fmt.Printf("3-5. rotated and re-encrypted %v (%s); new keys pushed to remaining members\n", rotated, total.Round(time.Second))
	fmt.Printf("6.   audit log written on the hub\n")
	fmt.Printf("7.   offboarding of %s complete in %s.\n", name, time.Since(start).Round(time.Second))
	fmt.Println("Note: offboarding does not recover data the person already downloaded. What was in their folder stays on their machine.") // FR-116
	return nil
}

func cmdBackup(args []string) error {
	api, _, _, err := client()
	if err != nil {
		return err
	}
	if len(args) < 1 || args[0] != "verify" {
		usage()
	}
	var b map[string]any
	if err := api.Get("/op/backup", &b); err != nil {
		return err
	}
	pj, _ := json.MarshalIndent(b, "", "  ")
	fmt.Println(string(pj))
	last, _ := b["last"].(map[string]any)
	can, _ := b["canary"].(map[string]any)
	if last == nil {
		return errors.New("no backup has completed yet")
	}
	if fmt.Sprint(last["result"]) != "ok" {
		return errors.New("last backup did not succeed")
	}
	if can == nil || fmt.Sprint(can["result"]) != "ok" {
		return errors.New("backup canary has not verified")
	}
	fmt.Println("backup: ok")
	return nil
}

func cmdAudit(args []string) error {
	api, _, _, err := client()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	limit := fs.Int("limit", 200, "")
	_ = fs.Parse(args)
	var a []model.AuditEntry
	if err := api.Get("/op/audit?limit="+strconv.Itoa(*limit), &a); err != nil {
		return err
	}
	for i := len(a) - 1; i >= 0; i-- {
		e := a[i]
		fmt.Printf("%s %-10s %-22s %-14s %s\n", e.Timestamp.Local().Format("2006-01-02 15:04:05"), e.Operator, e.Action, e.Target, e.Detail)
	}
	return nil
}

func cmdSettings(args []string) error {
	api, _, _, err := client()
	if err != nil {
		return err
	}
	kv := map[string]string{}
	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			return errors.New("usage: qpctl settings key=value ...  (sync_interval=60, open_signup=1|0, signup_quota=<bytes>)")
		}
		kv[k] = v
	}
	return api.Post("/op/settings", kv, nil)
}
