// Package tlsutil loads externally issued server certificates (PEM chain + key) and reloads them when the files
// change, so a renewed certificate takes effect without restarting the server.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Reloader struct {
	name, certFile, keyFile string
	interval                time.Duration
	mu                      sync.Mutex
	cert                    *tls.Certificate
	leaf                    *x509.Certificate
	certMod, keyMod         time.Time
	lastCheck               time.Time
}

// NewReloader loads and validates the pair; it fails if the files are unreadable, mismatched or already expired.
// name labels log messages (e.g. "admin").
func NewReloader(name, certFile, keyFile string) (*Reloader, error) {
	r := &Reloader{name: name, certFile: certFile, keyFile: keyFile, interval: 10 * time.Second}
	if err := r.load(); err != nil {
		return nil, fmt.Errorf("%s tls: %w", name, err)
	}
	return r, nil
}

func modTimes(certFile, keyFile string) (c, k time.Time, err error) {
	ci, err := os.Stat(certFile)
	if err != nil {
		return
	}
	ki, err := os.Stat(keyFile)
	if err != nil {
		return
	}
	return ci.ModTime(), ki.ModTime(), nil
}

func (r *Reloader) load() error {
	cm, km, err := modTimes(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	kp, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("%v (cert %s, key %s; the key must be unencrypted PEM and match the certificate)", err, r.certFile, r.keyFile)
	}
	leaf, err := x509.ParseCertificate(kp.Certificate[0])
	if err != nil {
		return err
	}
	if time.Now().After(leaf.NotAfter) {
		return fmt.Errorf("certificate expired %s", leaf.NotAfter.Format(time.RFC3339))
	}
	if time.Now().Before(leaf.NotBefore) {
		return fmt.Errorf("certificate is not valid until %s", leaf.NotBefore.Format(time.RFC3339))
	}
	kp.Leaf = leaf
	r.cert, r.leaf, r.certMod, r.keyMod = &kp, leaf, cm, km
	return nil
}

// GetCertificate is for tls.Config.GetCertificate. It re-reads the files when their modification time changes;
// a bad replacement is logged and ignored so the last good certificate keeps being served.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := time.Now(); now.Sub(r.lastCheck) >= r.interval {
		r.lastCheck = now
		if cm, km, err := modTimes(r.certFile, r.keyFile); err == nil && (!cm.Equal(r.certMod) || !km.Equal(r.keyMod)) {
			old := r.leaf
			if err := r.load(); err != nil {
				log.Printf("%s tls: new certificate rejected, still serving the previous one: %v", r.name, err)
				r.certMod, r.keyMod = cm, km // do not retry every request until the files change again
			} else {
				log.Printf("%s tls: loaded new certificate (%s), replacing serial %s", r.name, r.Describe(), old.SerialNumber.Text(16))
			}
		}
	}
	return r.cert, nil
}

// Describe summarises the current certificate for logs.
func (r *Reloader) Describe() string {
	l := r.leaf
	names := append([]string{}, l.DNSNames...)
	for _, ip := range l.IPAddresses {
		names = append(names, ip.String())
	}
	sort.Strings(names)
	return fmt.Sprintf("subject=%q issuer=%q names=[%s] expires=%s", l.Subject.CommonName, l.Issuer.CommonName, strings.Join(names, ","), l.NotAfter.Format("2006-01-02"))
}

// Check reports problems worth warning about: expiry soon, or names that the certificate does not cover.
func (r *Reloader) Check(names []string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var w []string
	if d := time.Until(r.leaf.NotAfter); d < 30*24*time.Hour {
		w = append(w, fmt.Sprintf("%s tls: certificate expires in %d days", r.name, int(d.Hours()/24)))
	}
	for _, n := range names {
		if n != "" && r.leaf.VerifyHostname(n) != nil {
			w = append(w, fmt.Sprintf("%s tls: certificate does not cover %q (clients using that name will fail TLS verification)", r.name, n))
		}
	}
	return w
}

// Pair is a cert/key file pair from configuration.
type Pair struct{ Cert, Key string }

func (p Pair) Set() bool { return p.Cert != "" || p.Key != "" }

func (p Pair) Validate(name string) error {
	if (p.Cert == "") != (p.Key == "") {
		return errors.New(name + ": both a certificate and a key file are required")
	}
	return nil
}
