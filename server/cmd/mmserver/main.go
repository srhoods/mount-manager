// mmserver: Mount Manager server.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"github.com/rhoods/mountmanager/internal/auth"
	"github.com/rhoods/mountmanager/internal/db"
	"golang.org/x/term"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhoods/mountmanager/internal/api"
	"github.com/rhoods/mountmanager/internal/pki"
	"github.com/rhoods/mountmanager/internal/tlsutil"
)

// Set at build time: -ldflags "-X main.version=... -X main.commit=... -X main.built=...".
var (
	version = "dev"
	commit  = "unknown"
	built   = "unknown"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("MM_DSN"), "postgres DSN (or MM_DSN)")
	names := flag.String("names", "localhost", "comma-separated server cert names/IPs")
	agentAddr := flag.String("agent-listen", ":8443", "agent mTLS listen address")
	adminAddr := flag.String("admin-listen", ":8444", "admin API/UI listen address")
	initAdmin := flag.String("init-admin", "", "create/reset local admin user 'admin' with this password and exit")
	tlsCert := flag.String("tls-cert", os.Getenv("MM_TLS_CERT"), "PEM certificate chain from your own CA, used for BOTH listeners unless overridden (or MM_TLS_CERT)")
	tlsKey := flag.String("tls-key", os.Getenv("MM_TLS_KEY"), "PEM private key for -tls-cert (or MM_TLS_KEY)")
	adminCert := flag.String("admin-tls-cert", os.Getenv("MM_ADMIN_TLS_CERT"), "certificate chain for the admin/web listener only (or MM_ADMIN_TLS_CERT)")
	adminKey := flag.String("admin-tls-key", os.Getenv("MM_ADMIN_TLS_KEY"), "private key for -admin-tls-cert (or MM_ADMIN_TLS_KEY)")
	agentCert := flag.String("agent-tls-cert", os.Getenv("MM_AGENT_TLS_CERT"), "certificate chain for the agent listener only (or MM_AGENT_TLS_CERT)")
	agentKey := flag.String("agent-tls-key", os.Getenv("MM_AGENT_TLS_KEY"), "private key for -agent-tls-cert (or MM_AGENT_TLS_KEY)")
	ldapCfg := flag.String("ldap-config", os.Getenv("MM_LDAP_CONFIG"), "LDAPS auth config JSON (or MM_LDAP_CONFIG); optional")
	showVersion := flag.Bool("version", false, "print version and exit")
	ldapCheck := flag.String("ldap-check", "", "test directory login for this user (password from MM_LDAP_TEST_PASSWORD or stdin) and exit; needs no database")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	if strings.HasPrefix(*ldapCfg, "-") {
		log.Fatalf("-ldap-config needs a file path but was given %q.\nUsage: mmserver -ldap-config /etc/mountmgr-server/ldap.json -ldap-check <username>", *ldapCfg)
	}
	if *ldapCheck != "" {
		os.Exit(checkLDAP(*ldapCfg, *ldapCheck))
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := pool.Exec(ctx, db.Schema); err != nil {
		log.Fatalf("schema: %v", err)
	}
	if *initAdmin != "" {
		_, err := pool.Exec(ctx, `INSERT INTO users(username,pass_hash,role) VALUES('admin',$1,'admin')
		  ON CONFLICT (username) DO UPDATE SET pass_hash=$1`, api.HashPassword(*initAdmin))
		if err != nil {
			log.Fatal(err)
		}
		log.Print("admin user set")
		return
	}

	ca := loadOrCreateCA(ctx, pool)
	nameList := strings.Split(*names, ",")
	pool2 := x509.NewCertPool()
	pool2.AddCert(ca.Cert) // client certificates are always issued by the built-in CA

	// Each listener serves either a certificate from your own CA (reloaded when the files change) or the
	// built-in CA's certificate. Nothing is generated unless a listener needs it.
	var builtin *tls.Certificate
	serverCert := func(name string, specific, shared tlsutil.Pair) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		pr := shared
		if specific.Set() {
			pr = specific
		}
		if err := pr.Validate(name + " listener"); err != nil {
			log.Fatal(err)
		}
		if pr.Set() {
			r, err := tlsutil.NewReloader(name, pr.Cert, pr.Key)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("%s listener: external certificate %s", name, r.Describe())
			for _, w := range r.Check(nameList) {
				log.Printf("WARNING %s", w)
			}
			return r.GetCertificate
		}
		if builtin == nil {
			kp, err := loadOrCreateServerCert(ctx, pool, ca, nameList)
			if err != nil {
				log.Fatal(err)
			}
			builtin = &kp
		}
		log.Printf("%s listener: built-in CA certificate", name)
		return func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return builtin, nil }
	}
	shared := tlsutil.Pair{Cert: *tlsCert, Key: *tlsKey}
	adminGet := serverCert("admin", tlsutil.Pair{Cert: *adminCert, Key: *adminKey}, shared)
	agentGet := serverCert("agent", tlsutil.Pair{Cert: *agentCert, Key: *agentKey}, shared)

	s := api.New(pool, ca)
	s.Info = api.BuildInfo{Version: version, Commit: commit, Built: built, Started: time.Now().UTC()}
	if *ldapCfg != "" {
		cfg, err := auth.LoadConfig(*ldapCfg)
		if err != nil {
			log.Fatalf("ldap config: %v", err)
		}
		d, err := auth.New(cfg)
		if err != nil {
			log.Fatalf("ldap config: %v", err)
		}
		s.Dir = d
		log.Printf("directory login enabled (%s)", cfg.URL)
	}
	agentSrv := &http.Server{Addr: *agentAddr, Handler: s.AgentMux(), TLSConfig: &tls.Config{
		GetCertificate: agentGet, ClientCAs: pool2, ClientAuth: tls.VerifyClientCertIfGiven, MinVersion: tls.VersionTLS12}}
	adminSrv := &http.Server{Addr: *adminAddr, Handler: s.AdminMux(), TLSConfig: &tls.Config{
		GetCertificate: adminGet, MinVersion: tls.VersionTLS12}}
	go func() { log.Fatal(adminSrv.ListenAndServeTLS("", "")) }()
	log.Printf("mmserver %s (commit %s, built %s): agent %s admin %s", version, commit, built, *agentAddr, *adminAddr)
	log.Fatal(agentSrv.ListenAndServeTLS("", ""))
}

func loadOrCreateCA(ctx context.Context, db *pgxpool.Pool) *pki.CA {
	var c, k []byte
	if db.QueryRow(ctx, `SELECT v FROM kv WHERE k='ca_cert'`).Scan(&c) == nil &&
		db.QueryRow(ctx, `SELECT v FROM kv WHERE k='ca_key'`).Scan(&k) == nil {
		ca, err := pki.LoadCA(c, k)
		if err != nil {
			log.Fatal(err)
		}
		return ca
	}
	c, k, err := pki.NewCA()
	if err != nil {
		log.Fatal(err)
	}
	db.Exec(ctx, `INSERT INTO kv VALUES('ca_cert',$1),('ca_key',$2)`, c, k)
	log.Print("generated new CA")
	ca, _ := pki.LoadCA(c, k)
	return ca
}

// checkLDAP lets an operator validate directory settings and role mapping before enabling them.
func checkLDAP(path, user string) int {
	if path == "" {
		fmt.Fprintln(os.Stderr, "need -ldap-config")
		return 2
	}
	cfg, err := auth.LoadConfig(path)
	if err == nil {
		var d *auth.LDAP
		if d, err = auth.New(cfg); err == nil {
			d.Trace = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "  - "+f+"\n", a...) }
			pw := os.Getenv("MM_LDAP_TEST_PASSWORD")
			switch {
			case pw != "":
			case term.IsTerminal(int(os.Stdin.Fd())):
				fmt.Fprintf(os.Stderr, "Password for %s: ", user)
				b, perr := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if perr != nil {
					fmt.Fprintf(os.Stderr, "reading password: %v\n", perr)
					return 2
				}
				pw = string(b)
			default: // piped: read it from standard input
				b, _ := io.ReadAll(os.Stdin)
				pw = strings.TrimRight(string(b), "\r\n")
			}
			res, aerr := d.Authenticate(user, pw)
			if res != nil {
				fmt.Printf("DN:     %s\nGroups: %s\nRole:   %s\n", res.DN, strings.Join(res.Groups, "\n        "), res.Role)
			}
			if aerr != nil {
				fmt.Fprintf(os.Stderr, "FAILED: %v\n", aerr)
				return 1
			}
			fmt.Println("OK")
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "config error: %v\n", err)
	return 2
}

// loadOrCreateServerCert keeps the server certificate stable across restarts (so browsers and
// pinned clients keep trusting it); it is reissued only when the configured names change or it nears expiry.
func loadOrCreateServerCert(ctx context.Context, db *pgxpool.Pool, ca *pki.CA, names []string) (tls.Certificate, error) {
	want := strings.Join(names, ",")
	var c, k, n []byte
	if db.QueryRow(ctx, `SELECT v FROM kv WHERE k='srv_cert'`).Scan(&c) == nil &&
		db.QueryRow(ctx, `SELECT v FROM kv WHERE k='srv_key'`).Scan(&k) == nil &&
		db.QueryRow(ctx, `SELECT v FROM kv WHERE k='srv_names'`).Scan(&n) == nil && string(n) == want {
		if kp, err := tls.X509KeyPair(c, k); err == nil && len(kp.Certificate) > 0 {
			if x, err := x509.ParseCertificate(kp.Certificate[0]); err == nil && time.Until(x.NotAfter) > 30*24*time.Hour {
				return kp, nil
			}
		}
	}
	c, k, err := ca.ServerCert(names)
	if err != nil {
		return tls.Certificate{}, err
	}
	for key, v := range map[string][]byte{"srv_cert": c, "srv_key": k, "srv_names": []byte(want)} {
		if _, err := db.Exec(ctx, `INSERT INTO kv VALUES($1,$2) ON CONFLICT (k) DO UPDATE SET v=$2`, key, v); err != nil {
			return tls.Certificate{}, err
		}
	}
	log.Printf("issued new server certificate for %s", want)
	return tls.X509KeyPair(c, k)
}
