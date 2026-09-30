package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePair(t *testing.T, dir, cn string, dns []string, notAfter time.Time) (cert, key string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: cn}, DNSNames: dns,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	cert, key = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600)
	return
}

func bump(t *testing.T, files ...string) { // make the modification time visibly newer
	t.Helper()
	future := time.Now().Add(time.Minute)
	for _, f := range files {
		os.Chtimes(f, future, future)
		future = future.Add(time.Second)
	}
}

func serial(r *Reloader) string {
	c, _ := r.GetCertificate(nil)
	return c.Leaf.SerialNumber.Text(16)
}

func TestLoadAndDescribe(t *testing.T) {
	d := t.TempDir()
	c, k := writePair(t, d, "mm.example.com", []string{"mm.example.com", "*.lab.example.com"}, time.Now().Add(90*24*time.Hour))
	r, err := NewReloader("admin", c, k)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Describe(), "mm.example.com") {
		t.Errorf("describe: %s", r.Describe())
	}
	if w := r.Check([]string{"mm.example.com", "host.lab.example.com"}); len(w) != 0 {
		t.Errorf("unexpected warnings: %v", w)
	}
	if w := r.Check([]string{"other.example.org"}); len(w) != 1 || !strings.Contains(w[0], "does not cover") {
		t.Errorf("want a coverage warning: %v", w)
	}
}

func TestRejectsBadInput(t *testing.T) {
	d := t.TempDir()
	c, k := writePair(t, d, "x", []string{"x"}, time.Now().Add(time.Hour))
	d2 := t.TempDir()
	_, k2 := writePair(t, d2, "y", []string{"y"}, time.Now().Add(time.Hour))
	if _, err := NewReloader("a", c, k2); err == nil {
		t.Error("mismatched key must be rejected")
	}
	if _, err := NewReloader("a", c, filepath.Join(d, "missing.key")); err == nil {
		t.Error("missing key must be rejected")
	}
	d3 := t.TempDir()
	c3, k3 := writePair(t, d3, "old", []string{"old"}, time.Now().Add(-time.Minute))
	if _, err := NewReloader("a", c3, k3); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired certificate must be rejected: %v", err)
	}
	_ = k
}

func TestShortExpiryWarns(t *testing.T) {
	c, k := writePair(t, t.TempDir(), "x", []string{"x"}, time.Now().Add(5*24*time.Hour))
	r, _ := NewReloader("agent", c, k)
	if w := r.Check(nil); len(w) != 1 || !strings.Contains(w[0], "expires in") {
		t.Errorf("want expiry warning: %v", w)
	}
}

func TestHotReload(t *testing.T) {
	d := t.TempDir()
	c, k := writePair(t, d, "one", []string{"one"}, time.Now().Add(90*24*time.Hour))
	r, _ := NewReloader("admin", c, k)
	r.interval = 0
	first := serial(r)
	if serial(r) != first {
		t.Fatal("certificate changed without a file change")
	}
	writePair(t, d, "two", []string{"two"}, time.Now().Add(90*24*time.Hour)) // renewal: same file paths
	bump(t, c, k)
	if got := serial(r); got == first {
		t.Fatal("renewed certificate was not picked up")
	}
}

func TestBadReplacementKeepsServingOldCert(t *testing.T) {
	d := t.TempDir()
	c, k := writePair(t, d, "one", []string{"one"}, time.Now().Add(90*24*time.Hour))
	r, _ := NewReloader("admin", c, k)
	r.interval = 0
	first := serial(r)
	os.WriteFile(c, []byte("not a certificate"), 0o644)
	bump(t, c, k)
	if serial(r) != first {
		t.Fatal("a broken replacement must not displace the working certificate")
	}
	// fixing the files later is picked up
	writePair(t, d, "three", []string{"three"}, time.Now().Add(90*24*time.Hour))
	bump(t, c, k)
	if serial(r) == first {
		t.Fatal("a later good certificate should be loaded")
	}
}

func TestPairValidate(t *testing.T) {
	if (Pair{Cert: "a"}).Validate("x") == nil || (Pair{Key: "b"}).Validate("x") == nil {
		t.Error("half a pair must be rejected")
	}
	if (Pair{}).Validate("x") != nil || (Pair{Cert: "a", Key: "b"}).Validate("x") != nil {
		t.Error("empty or complete pairs are fine")
	}
	if (Pair{}).Set() || !(Pair{Cert: "a", Key: "b"}).Set() {
		t.Error("Set")
	}
}
