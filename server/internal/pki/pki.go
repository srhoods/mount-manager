// Package pki implements the built-in CA used for agent mTLS enrolment.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"time"
)

type CA struct {
	Cert    *x509.Certificate
	CertPEM []byte
	key     *ecdsa.PrivateKey
}

func newKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}

// NewCA creates a fresh CA; returns cert and key PEM for persistence.
func NewCA() (certPEM, keyPEM []byte, err error) {
	k, err := newKey()
	if err != nil {
		return nil, nil, err
	}
	t := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Mount Manager CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, t, t, &k.PublicKey, k)
	if err != nil {
		return nil, nil, err
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), nil
}

func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("bad CA pem")
	}
	c, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: c, CertPEM: certPEM, key: k}, nil
}

// SignCSR issues a client cert with CN=hostname.
func (ca *CA) SignCSR(csrPEM []byte, hostname string, ttl time.Duration) (certPEM []byte, serialHex string, err error) {
	b, _ := pem.Decode(csrPEM)
	if b == nil {
		return nil, "", errors.New("bad CSR pem")
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return nil, "", errors.New("invalid CSR")
	}
	s := serial()
	t := &x509.Certificate{SerialNumber: s, Subject: pkix.Name{CommonName: hostname, OrganizationalUnit: []string{"mm-agent"}},
		DNSNames: []string{hostname}, NotBefore: time.Now().Add(-5 * time.Minute), NotAfter: time.Now().Add(ttl),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, t, ca.Cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), s.Text(16), nil
}

// ServerCert issues a server cert for the given names (signed by the CA).
func (ca *CA) ServerCert(names []string) (certPEM, keyPEM []byte, err error) {
	k, err := newKey()
	if err != nil {
		return nil, nil, err
	}
	var dns []string
	var ips []net.IP
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, n)
		}
	}
	t := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: names[0]}, DNSNames: dns, IPAddresses: ips,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(2, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, t, ca.Cert, &k.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), nil
}
