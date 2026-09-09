package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
	"quietport.app/quietport/internal/shamir"
)

// keys export / split / recover / verify: FR-120..124.

type bundleFile struct {
	Format string `json:"format"` // qp-keys-v1
	Salt   string `json:"salt"`   // base64, passphrase mode
	Mode   string `json:"mode"`   // passphrase | shamir
	Data   string `json:"data"`   // base64 ciphertext of Export JSON
}

func readPassphrase(prompt string, confirm bool) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(b) < 12 {
		return "", errors.New("use at least 12 characters")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "again: ")
		c, _ := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if string(c) != string(b) {
			return "", errors.New("passphrases differ")
		}
	}
	return string(b), nil
}

func cmdKeys(args []string) error {
	if len(args) < 1 {
		usage()
	}
	switch args[0] {
	case "export":
		fs := flag.NewFlagSet("keys export", flag.ExitOnError)
		out := fs.String("out", "", "output file")
		_ = fs.Parse(args[1:])
		if *out == "" {
			return errors.New("--out required")
		}
		ks, err := openKeystore()
		if err != nil {
			return err
		}
		pass, err := readPassphrase("passphrase for the export: ", true)
		if err != nil {
			return err
		}
		pt, _ := json.Marshal(Export{ExportedAt: time.Now(), Keys: ks.Keys})
		salt := cryptobox.RandomBytes(16)
		ct, err := cryptobox.EncryptWithKey(cryptobox.PassphraseKey(pass, salt), pt)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(bundleFile{Format: "qp-keys-v1", Mode: "passphrase", Salt: base64.StdEncoding.EncodeToString(salt), Data: base64.StdEncoding.EncodeToString(ct)}, "", "  ")
		if err := os.WriteFile(*out, b, 0o600); err != nil {
			return err
		}
		ks.LastExport = time.Now()
		_ = ks.save()
		fmt.Printf("wrote %s (%d circle(s)). Store it offline. Without a copy of these keys the circles are unrecoverable, by design.\n", *out, len(ks.Keys))
	case "split":
		fs := flag.NewFlagSet("keys split", flag.ExitOnError)
		out := fs.String("out", "", "output directory")
		shares := fs.Int("shares", 5, "M")
		threshold := fs.Int("threshold", 3, "N")
		_ = fs.Parse(args[1:])
		if *out == "" {
			return errors.New("--out required")
		}
		if *threshold < 2 || *shares < *threshold || *shares > 255 {
			return errors.New("need 2 <= threshold <= shares <= 255")
		}
		ks, err := openKeystore()
		if err != nil {
			return err
		}
		_ = os.MkdirAll(*out, 0o700)
		master := cryptobox.RandomBytes(32)
		pt, _ := json.Marshal(Export{ExportedAt: time.Now(), Keys: ks.Keys})
		ct, err := cryptobox.EncryptWithKey(master, pt)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(bundleFile{Format: "qp-keys-v1", Mode: "shamir", Data: base64.StdEncoding.EncodeToString(ct)}, "", "  ")
		if err := os.WriteFile(filepath.Join(*out, "quietport-keys.bundle"), b, 0o600); err != nil {
			return err
		}
		parts, err := shamir.Split(master, *shares, *threshold)
		if err != nil {
			return err
		}
		for i, p := range parts {
			name := filepath.Join(*out, fmt.Sprintf("share-%d-of-%d.txt", i+1, *shares))
			body := fmt.Sprintf("Quietport key share %d of %d (any %d recover the bundle)\n%s\n", i+1, *shares, *threshold, base64.StdEncoding.EncodeToString(p))
			if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
				return err
			}
		}
		ks.LastExport = time.Now()
		_ = ks.save()
		fmt.Printf("wrote quietport-keys.bundle and %d shares to %s. Give each share to a different holder; keep the bundle with any of them or separately. Any %d shares plus the bundle recover every key.\n", *shares, *out, *threshold)
	case "recover":
		fs := flag.NewFlagSet("keys recover", flag.ExitOnError)
		bundle := fs.String("bundle", "", "")
		out := fs.String("out", "", "write recovered keys (JSON) here; omit to import into this keystore")
		var shareFiles stringList
		fs.Var(&shareFiles, "share", "share file (repeat)")
		_ = fs.Parse(args[1:])
		exp, err := openBundle(*bundle, shareFiles)
		if err != nil {
			return err
		}
		if *out != "" {
			b, _ := json.MarshalIndent(exp, "", "  ")
			return os.WriteFile(*out, b, 0o600)
		}
		ks, err := openKeystore()
		if err != nil {
			return err
		}
		n := 0
		for slug, keys := range exp.Keys {
			for _, k := range keys {
				if _, have := ks.Gen(slug, k.Generation); !have {
					ks.Add(k)
					n++
				}
			}
		}
		if err := ks.save(); err != nil {
			return err
		}
		fmt.Printf("imported %d key(s) into this keystore\n", n)
	case "verify":
		fs := flag.NewFlagSet("keys verify", flag.ExitOnError)
		bundle := fs.String("bundle", "", "exported bundle to test (default: use the live keystore)")
		var shareFiles stringList
		fs.Var(&shareFiles, "share", "share file (repeat, shamir bundles)")
		_ = fs.Parse(args[1:])
		return verifyKeys(*bundle, shareFiles)
	default:
		usage()
	}
	return nil
}

func openBundle(path string, shareFiles []string) (Export, error) {
	var exp Export
	raw, err := os.ReadFile(path)
	if err != nil {
		return exp, err
	}
	var bf bundleFile
	if err := json.Unmarshal(raw, &bf); err != nil || bf.Format != "qp-keys-v1" {
		return exp, errors.New("not a Quietport key bundle")
	}
	ct, err := base64.StdEncoding.DecodeString(bf.Data)
	if err != nil {
		return exp, err
	}
	var key []byte
	switch bf.Mode {
	case "passphrase":
		pass, err := readPassphrase("bundle passphrase: ", false)
		if err != nil {
			return exp, err
		}
		salt, _ := base64.StdEncoding.DecodeString(bf.Salt)
		key = cryptobox.PassphraseKey(pass, salt)
	case "shamir":
		if len(shareFiles) < 2 {
			return exp, errors.New("shamir bundle: pass --share files (at least the threshold)")
		}
		var parts [][]byte
		for _, f := range shareFiles {
			b, err := os.ReadFile(f)
			if err != nil {
				return exp, err
			}
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			p, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[len(lines)-1]))
			if err != nil {
				return exp, fmt.Errorf("%s: bad share", f)
			}
			parts = append(parts, p)
		}
		key, err = shamir.Combine(parts)
		if err != nil {
			return exp, err
		}
	default:
		return exp, errors.New("unknown bundle mode")
	}
	pt, err := cryptobox.DecryptWithKey(key, ct)
	if err != nil {
		return exp, errors.New("bundle did not decrypt (wrong passphrase or shares)")
	}
	return exp, json.Unmarshal(pt, &exp)
}

// verifyKeys: FR-122. A real decryption of each circle's canary object, using ONLY the keys from the bundle.
func verifyKeys(bundle string, shareFiles []string) error {
	api, ks, conf, err := client()
	if err != nil {
		return err
	}
	keys := ks.Keys
	source := "live keystore"
	if bundle != "" {
		exp, err := openBundle(bundle, shareFiles)
		if err != nil {
			return err
		}
		keys = exp.Keys
		source = bundle
	}
	var cs []model.Circle
	if err := api.Get("/op/circles", &cs); err != nil {
		return err
	}
	failed := 0
	for _, c := range cs {
		var key model.CircleKey
		found := false
		for _, k := range keys[c.Slug] {
			if k.Generation == c.Generation {
				key, found = k, true
			}
		}
		if !found {
			fmt.Printf("  %-14s FAIL: %s has no key for generation %d\n", c.Slug, source, c.Generation)
			failed++
			continue
		}
		var k opKey
		if err := api.Post("/op/circles/"+c.Slug+"/opkey", nil, &k); err != nil {
			return err
		}
		env, err := rcloneEnv(conf, k, map[string]model.CircleKey{"c": key})
		if err == nil && !storageReachable(conf, k.Endpoint) {
			err = errStorageUnreachable
		}
		if err == nil {
			out, rerr := runRclone(env, "cat", "c:"+canaryName)
			err = rerr
			if err == nil && out != canaryText(c.Slug) {
				err = errors.New("canary decrypted to unexpected content")
			}
		}
		_ = api.Delete("/op/circles/" + c.Slug + "/opkey/" + k.AccessKey)
		if err != nil {
			fmt.Printf("  %-14s FAIL: %v\n", c.Slug, err)
			failed++
			continue
		}
		fmt.Printf("  %-14s ok (generation %d decrypts)\n", c.Slug, c.Generation)
	}
	if failed > 0 {
		return fmt.Errorf("%d circle(s) failed verification against %s", failed, source)
	}
	ks.LastVerify = time.Now()
	_ = ks.save()
	fmt.Printf("all %d circle(s) verified against %s\n", len(cs), source)
	return nil
}
