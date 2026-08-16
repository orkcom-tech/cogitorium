package gearnet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The certificate authority this gate signs with when it has to read inside a
// granted run's TLS.
//
// It exists for one reason: a secret this install holds must not be inside the
// process that uses it. The gate substitutes the real value into the outbound
// request, and it cannot substitute into bytes it cannot read. A CONNECT tunnel
// is opaque by design, so for a run holding references — and ONLY for such a
// run — the gate terminates TLS itself, rewrites, and opens its own TLS
// connection to the real destination.
//
// What that costs is stated rather than buried: for those runs the gate sees
// the plaintext of the gear's requests. It is the operator's own proxy on the
// operator's own machine, and it is the same boundary that already decides
// which hosts may be reached at all — but a proxy that reads request bodies is
// a different thing from one that counts bytes, and nobody should discover that
// from a changelog.
//
// The private key never leaves this machine and is never handed to a gear. The
// gear is given the certificate, which lets it verify the gate and nothing
// else.
type authority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte

	mu     sync.Mutex
	issued map[string]*tls.Certificate
}

// loadAuthority reads the install's CA, creating it on first use.
//
// Kept beside the database rather than regenerated per start: a gear that
// cached the certificate, or an operator who added it to a base image, must not
// find it changed underneath them on every restart.
func loadAuthority(dir string) (*authority, error) {
	certPath := filepath.Join(dir, "gearnet-ca.crt")
	keyPath := filepath.Join(dir, "gearnet-ca.key")

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		a, err := parseAuthority(certPEM, keyPEM)
		if err == nil {
			return a, nil
		}
		// A half-written or corrupt pair is replaced rather than fatal: it is
		// this install's own scratch credential, nothing signed by it outlives
		// a run, and refusing to start over it would be a server that will not
		// boot because of a file only it writes.
	}
	certPEM, keyPEM, err := newAuthority()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write the gate's signing key: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write the gate's certificate: %w", err)
	}
	return parseAuthority(certPEM, keyPEM)
}

func newAuthority() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Cogitorium gear network gate",
			Organization: []string{"Cogitorium"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func parseAuthority(certPEM, keyPEM []byte) (*authority, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the gate's certificate and key do not go together: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the gate's signing key is %T, not the elliptic key it writes", pair.PrivateKey)
	}
	return &authority{cert: cert, key: key, pem: certPEM, issued: map[string]*tls.Certificate{}}, nil
}

// leaf returns a certificate for one host, minting and caching it on first ask.
//
// Cached because a gear making a hundred requests to one API would otherwise
// pay a key generation per connection, and that shows up as the gate being slow
// rather than as what it is.
func (a *authority) leaf(host string) (*tls.Certificate, error) {
	a.mu.Lock()
	if c, ok := a.issued[host]; ok {
		a.mu.Unlock()
		return c, nil
	}
	a.mu.Unlock()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		// Short, because these are minted for one install's own runs and a
		// long-lived certificate for somebody else's hostname is a thing worth
		// not having lying around.
		NotAfter:    time.Now().AddDate(0, 1, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := parseIP(host); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	} else {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, err
	}
	out := &tls.Certificate{Certificate: [][]byte{der, a.cert.Raw}, PrivateKey: key, Leaf: tmpl}

	a.mu.Lock()
	a.issued[host] = out
	a.mu.Unlock()
	return out, nil
}
