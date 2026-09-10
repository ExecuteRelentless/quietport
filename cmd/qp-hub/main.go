// qp-hub: the Quietport hub. One binary (SRD §12): invite service + agent API + operator API + TLS front for Headscale.
package main

import (
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/hubdb"
)

var Version = "dev" // -ldflags "-X main.Version=..."

//go:embed all:web
var webFS embed.FS

type Hub struct {
	cfg  Config
	db   *hubdb.DB
	hs   Headscale
	gar  *Garage
	site fs.FS
}

type Config struct {
	Host           string   // public hostname (invite service, headscale)
	ExtraHosts     []string // extra TLS hosts (e.g. hub.quietport.app + quietport.app)
	TailnetIP      string
	DBPath         string
	ReleasesDir    string
	PolicyDir      string
	GarageConfig   string
	ACMEDir        string
	HeadscaleURL   string
	OperatorName   string
	SupportContact string
	AgentAPIPort   string
	S3Port         string
	NoTLS          bool // dev only
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadConfig() Config {
	c := Config{
		Host:           env("QP_HOST", ""),
		TailnetIP:      env("QP_TAILNET_IP", ""),
		DBPath:         env("QP_DB", "/var/lib/quietport/hub.db"),
		ReleasesDir:    env("QP_RELEASES", "/var/lib/quietport/releases"),
		PolicyDir:      env("QP_POLICY_DIR", "/var/lib/quietport/policy"),
		GarageConfig:   env("QP_GARAGE_CONFIG", "/etc/garage/garage.toml"),
		ACMEDir:        env("QP_ACME_DIR", "/var/lib/quietport/acme"),
		HeadscaleURL:   env("QP_HEADSCALE_URL", "http://127.0.0.1:8080"),
		OperatorName:   env("QP_OPERATOR_NAME", "Quietport"), // members only ever see the service's name
		SupportContact: env("QP_SUPPORT_CONTACT", ""),
		AgentAPIPort:   env("QP_AGENT_API_PORT", "8443"),
		S3Port:         env("QP_S3_PORT", "3900"),
		NoTLS:          os.Getenv("QP_NO_TLS") == "1",
	}
	if x := os.Getenv("QP_EXTRA_HOSTS"); x != "" {
		for _, h := range strings.Split(x, ",") {
			if h = strings.TrimSpace(h); h != "" {
				c.ExtraHosts = append(c.ExtraHosts, h)
			}
		}
	}
	return c
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("qp-hub ")
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: qp-hub serve | init-operator | release add ... | version")
		os.Exit(2)
	}
	cfg := loadConfig()
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	case "serve":
		if err := serve(cfg); err != nil {
			log.Fatal(err)
		}
	case "init-operator":
		db, err := hubdb.Open(cfg.DBPath)
		if err != nil {
			log.Fatal(err)
		}
		tok := cryptobox.NewToken()
		if err := db.SetSetting("operator_token_hash", cryptobox.HashToken(tok)); err != nil {
			log.Fatal(err)
		}
		db.Audit("hub", "operator.token.rotate", "hub", "")
		fmt.Println(tok)
	case "release":
		if err := releaseCmd(cfg, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		os.Exit(2)
	}
}

// release add --os darwin --arch arm64 --version 1.0.0 --file quietport-darwin-arm64.tar.gz --sig <b64>
func releaseCmd(cfg Config, args []string) error {
	if len(args) < 1 || args[0] != "add" {
		return errors.New("usage: qp-hub release add --os X --arch Y --version V --file F --sig S")
	}
	fsx := flag.NewFlagSet("release add", flag.ContinueOnError)
	osn := fsx.String("os", "", "")
	arch := fsx.String("arch", "", "")
	ver := fsx.String("version", "", "")
	file := fsx.String("file", "", "file name inside the releases dir")
	sig := fsx.String("sig", "", "base64 ed25519 signature over the sha256 hex")
	if err := fsx.Parse(args[1:]); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(cfg.ReleasesDir, *file))
	if err != nil {
		return err
	}
	db, err := hubdb.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	r := hubdb.Release{OS: *osn, Arch: *arch, Version: *ver, File: *file, SHA256: cryptobox.SHA256Hex(b), Sig: *sig}
	if err := db.ReleasePut(r); err != nil {
		return err
	}
	// latest symlink for /dl/
	latest := filepath.Join(cfg.ReleasesDir, fmt.Sprintf("quietport-%s-%s%s", *osn, *arch, ext(*file)))
	_ = os.Remove(latest)
	if err := os.Symlink(*file, latest); err != nil {
		return err
	}
	db.Audit("hub", "release.add", *osn+"/"+*arch, *ver+" "+r.SHA256)
	fmt.Println("published", *osn, *arch, *ver, r.SHA256)
	return nil
}

func ext(f string) string {
	if strings.HasSuffix(f, ".tar.gz") {
		return ".tar.gz"
	}
	return filepath.Ext(f)
}

func serve(cfg Config) error {
	if cfg.Host == "" {
		return errors.New("QP_HOST is required")
	}
	db, err := hubdb.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	gar, err := newGarage(cfg.GarageConfig)
	if err != nil {
		log.Printf("warning: %v (circle storage operations will fail until garage is configured)", err)
	}
	site, _ := fs.Sub(webFS, "web/site")
	h := &Hub{cfg: cfg, db: db, gar: gar, site: site}
	if db.Setting("operator_token_hash") == "" {
		log.Printf("no operator token yet: run `qp-hub init-operator`")
	}

	// --- public listener: site, invites, downloads, headscale proxy ---
	pub := http.NewServeMux()
	h.routesPublic(pub)
	hsURL, _ := url.Parse(cfg.HeadscaleURL)
	proxy := httputil.NewSingleHostReverseProxy(hsURL)
	proxy.ErrorLog = log.Default()
	pubHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := pub.Handler(r); pattern != "" {
			pub.ServeHTTP(w, r)
			return
		}
		// everything we do not serve ourselves is the Tailscale control protocol (/key, /ts2021, /machine/…)
		proxy.ServeHTTP(w, r)
	})
	pubSrv := &http.Server{Handler: secureHeaders(pubHandler), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}

	// --- agent + operator API on the tailnet address and loopback (never public: FR-71) ---
	api := http.NewServeMux()
	h.routesAgent(api)
	h.routesOperator(api)
	apiSrv := &http.Server{Handler: api, ReadHeaderTimeout: 15 * time.Second}
	apiAddrs := []string{"127.0.0.1:" + cfg.AgentAPIPort}
	if cfg.TailnetIP != "" {
		apiAddrs = append(apiAddrs, cfg.TailnetIP+":"+cfg.AgentAPIPort)
	}
	for _, a := range apiAddrs {
		ln, err := listenRetry(a, 60*time.Second)
		if err != nil {
			return fmt.Errorf("api listen %s: %w", a, err)
		}
		go func(l net.Listener) { log.Printf("api on %s", l.Addr()); _ = apiSrv.Serve(l) }(ln)
	}

	go h.housekeeping()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pubSrv.Shutdown(c)
		_ = apiSrv.Shutdown(c)
	}()

	if cfg.NoTLS {
		pubSrv.Addr = ":8080"
		log.Printf("public (dev, no TLS) on %s", pubSrv.Addr)
		err = pubSrv.ListenAndServe()
	} else {
		hosts := append([]string{cfg.Host}, cfg.ExtraHosts...)
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(hosts...),
			Cache:      autocert.DirCache(cfg.ACMEDir),
			Email:      cfg.SupportContact,
		}
		pubSrv.Addr = ":443"
		pubSrv.TLSConfig = m.TLSConfig()
		pubSrv.TLSConfig.MinVersion = tls.VersionTLS12
		log.Printf("public on :443 for %v", hosts)
		err = pubSrv.ListenAndServeTLS("", "")
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// listenRetry waits for the tailnet address to exist after boot.
func listenRetry(addr string, max time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(max)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil || time.Now().After(deadline) {
			return ln, err
		}
		time.Sleep(2 * time.Second)
	}
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// housekeeping: expire invites' preauth keys, prune heartbeats.
func (h *Hub) housekeeping() {
	t := time.NewTicker(10 * time.Minute)
	for range t.C {
		invs, err := h.db.Invites()
		if err != nil {
			continue
		}
		for _, i := range invs {
			if i.ConsumedAt == nil && !i.Revoked && time.Now().After(i.ExpiresAt) && i.SealedKeys != "" {
				_ = h.db.InviteRevoke(i.ID) // wipes sealed_keys; headscale expires the preauth key on its own clock
			}
		}
	}
}
