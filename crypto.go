package libp2pquic

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/gogo/protobuf/proto"
	ic "github.com/libp2p/go-libp2p-core/crypto"
	pb "github.com/libp2p/go-libp2p-core/crypto/pb"
)

// mint certificate selection is broken.
const hostname = "quic.ipfs"

const certValidityPeriod = 180 * 24 * time.Hour

func generateConfig(privKey ic.PrivKey) (*tls.Config, error) {
	key, hostCert, err := keyToCertificate(privKey)
	if err != nil {
		return nil, err
	}
	// The ephemeral key used just for a couple of connections (or a limited time).
	ephemeralKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	// Sign the ephemeral key using the host key.
	// This is the only time that the host's private key of the peer is needed.
	// Note that this step could be done asynchronously, such that a running node doesn't need access its private key at all.
	certTemplate := &x509.Certificate{
		DNSNames:     []string{hostname},
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(certValidityPeriod),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, certTemplate, hostCert, ephemeralKey.Public(), key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		ServerName:         hostname,
		InsecureSkipVerify: true, // This is not insecure here. We will verify the cert chain ourselves.
		ClientAuth:         tls.RequireAnyClientCert,
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{cert.Raw, hostCert.Raw},
			PrivateKey:  ephemeralKey,
		}},
	}, nil
}

func getRemotePubKey(chain []*x509.Certificate) (ic.PubKey, error) {
	if len(chain) != 2 {
		return nil, errors.New("expected 2 certificates in the chain")
	}
	pool := x509.NewCertPool()
	pool.AddCert(chain[1])
	if _, err := chain[0].Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		return nil, err
	}

	switch remotePubKey := chain[1].PublicKey.(type) {
	case *rsa.PublicKey:
		remotePubKeyPKIX, err := x509.MarshalPKIXPublicKey(remotePubKey)
		if err != nil {
			return nil, err
		}
		return ic.UnmarshalRsaPublicKey(remotePubKeyPKIX)
	case ed25519.PublicKey:
		return ic.UnmarshalEd25519PublicKey(remotePubKey)
	default:
		return nil, fmt.Errorf("unknown key type: %T", remotePubKey)
	}
}

func keyToCertificate(sk ic.PrivKey) (interface{}, *x509.Certificate, error) {
	sn, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().Add(certValidityPeriod),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	var publicKey interface{}
	var privateKey crypto.Signer
	keyBytes, err := sk.Bytes()
	if err != nil {
		return nil, nil, err
	}
	pbmes := new(pb.PrivateKey)
	if err := proto.Unmarshal(keyBytes, pbmes); err != nil {
		return nil, nil, err
	}
	switch pbmes.GetType() {
	case pb.KeyType_RSA:
		k, err := x509.ParsePKCS1PrivateKey(pbmes.GetData())
		if err != nil {
			return nil, nil, err
		}
		publicKey = &k.PublicKey
		privateKey = k
	case pb.KeyType_Ed25519:
		privateKey = ed25519.PrivateKey(pbmes.GetData())
		publicKey = privateKey.Public()
	// TODO: add support for ECDSA
	default:
		return nil, nil, errors.New("unsupported key type for TLS")
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, publicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}
	return privateKey, cert, nil
}
