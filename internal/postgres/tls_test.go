// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package postgres

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

const tlsTransportStop = "TLS transport test stops before authentication"

func TestVerifyFullTLSTransport(t *testing.T) {
	const hostname = "database.example.invalid"
	ca, key, trustedRoot := newTransportTestCA(t)
	_, _, unrelatedRoot := newTransportTestCA(t)
	certificate := newTransportTestCertificate(t, ca, key, hostname)
	for _, tc := range []struct {
		name, host, root string
		refuseTLS        bool
	}{
		{name: "trusted_hostname", host: hostname, root: trustedRoot},
		{name: "untrusted_ca", host: hostname, root: unrelatedRoot},
		{name: "hostname_mismatch", host: "other.example.invalid", root: trustedRoot},
		{name: "server_refuses_tls", host: hostname, root: trustedRoot, refuseTLS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			_, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			u := url.URL{
				Scheme: "postgresql", User: url.User("tls_probe"),
				Host: net.JoinHostPort(tc.host, port), Path: "/fixture",
				RawQuery: url.Values{"sslmode": {"verify-full"}, "sslrootcert": {tc.root}}.Encode(),
			}
			config, err := connectionConfig(u.String())
			if err != nil {
				t.Fatal(err)
			}
			if config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify || config.TLSConfig.ServerName != tc.host || len(config.Fallbacks) != 0 {
				t.Fatal("verify-full configuration lost hostname verification or added a fallback")
			}
			// Keep the remote-style hostname and its TLS policy, but never use DNS
			// or dial any endpoint other than this test's loopback listener.
			config.LookupFunc = func(_ context.Context, host string) ([]string, error) {
				if host != tc.host {
					return nil, errors.New("unexpected TLS test hostname lookup")
				}
				return []string{"127.0.0.1"}, nil
			}
			var attempts atomic.Int32
			config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
				if attempts.Add(1) != 1 || network != "tcp" || address != listener.Addr().String() {
					return nil, errors.New("unexpected TLS test dial or fallback attempt")
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			result := make(chan tlsTransportObservation, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					result <- tlsTransportObservation{err: err}
					return
				}
				result <- observeTLSTransport(conn, certificate, tc.refuseTLS)
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			conn, connectErr := pgconn.ConnectConfig(ctx, config)
			if conn != nil {
				_ = conn.Close(ctx)
				t.Fatal("transport-only server unexpectedly authenticated a connection")
			}
			if connectErr == nil {
				t.Fatal("transport-only server unexpectedly completed PostgreSQL startup")
			}
			var observed tlsTransportObservation
			select {
			case observed = <-result:
			case <-ctx.Done():
				t.Fatal("TLS test server did not finish")
			}
			if count := attempts.Load(); count != 1 {
				t.Fatalf("connection attempts = %d, want exactly one without fallback", count)
			}
			if !observed.sslRequested {
				t.Fatalf("PostgreSQL SSLRequest was not received: %v", observed.err)
			}
			switch tc.name {
			case "trusted_hostname":
				if observed.err != nil || !observed.tlsComplete || !observed.startupReceived || observed.serverName != hostname {
					t.Fatalf("trusted TLS did not reach encrypted startup: %+v", observed)
				}
				var protocolStop *pgconn.PgError
				if !errors.As(connectErr, &protocolStop) || protocolStop.Code != "08004" || protocolStop.Message != tlsTransportStop {
					t.Fatalf("expected deliberate PostgreSQL startup stop after valid TLS: %v", connectErr)
				}
			case "untrusted_ca":
				var unknownAuthority x509.UnknownAuthorityError
				if !errors.As(connectErr, &unknownAuthority) {
					t.Fatalf("expected untrusted CA rejection, got %v", connectErr)
				}
			case "hostname_mismatch":
				var hostnameError x509.HostnameError
				if !errors.As(connectErr, &hostnameError) || hostnameError.Host != tc.host {
					t.Fatalf("expected hostname verification rejection, got %v", connectErr)
				}
			case "server_refuses_tls":
				if observed.err != nil || !strings.Contains(connectErr.Error(), "server refused TLS connection") {
					t.Fatalf("TLS refusal did not close without plaintext startup: server=%v client=%v", observed.err, connectErr)
				}
			}
			if tc.name != "trusted_hostname" && (observed.tlsComplete || observed.startupReceived) {
				t.Fatal("rejected transport reached TLS completion or PostgreSQL startup")
			}
		})
	}
}

type tlsTransportObservation struct {
	sslRequested, tlsComplete, startupReceived bool
	serverName                                 string
	err                                        error
}

func observeTLSTransport(conn net.Conn, certificate tls.Certificate, refuseTLS bool) (result tlsTransportObservation) {
	defer conn.Close()
	if result.err = conn.SetDeadline(time.Now().Add(5 * time.Second)); result.err != nil {
		return result
	}
	message, err := pgproto3.NewBackend(conn, conn).ReceiveStartupMessage()
	if err != nil {
		result.err = err
		return result
	}
	if _, ok := message.(*pgproto3.SSLRequest); !ok {
		result.err = fmt.Errorf("expected SSLRequest before any startup data, got %T", message)
		return result
	}
	result.sslRequested = true
	response := byte('S')
	if refuseTLS {
		response = 'N'
	}
	if _, result.err = conn.Write([]byte{response}); result.err != nil {
		return result
	}
	if refuseTLS {
		var next [1]byte
		n, err := conn.Read(next[:])
		if n != 0 || !errors.Is(err, io.EOF) {
			result.err = fmt.Errorf("expected client close after TLS refusal, received %d bytes: %v", n, err)
		}
		return result
	}
	secured := tls.Server(conn, &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate},
	})
	defer secured.Close()
	if result.err = secured.Handshake(); result.err != nil {
		return result
	}
	state := secured.ConnectionState()
	result.tlsComplete, result.serverName = state.HandshakeComplete, state.ServerName
	backend := pgproto3.NewBackend(secured, secured)
	message, result.err = backend.ReceiveStartupMessage()
	if result.err != nil {
		return result
	}
	startup, ok := message.(*pgproto3.StartupMessage)
	if !ok || startup.Parameters["user"] != "tls_probe" || startup.Parameters["database"] != "fixture" {
		result.err = errors.New("unexpected encrypted PostgreSQL startup")
		return result
	}
	result.startupReceived = true
	// A protocol error ends this transport-only probe without pgconn's
	// asynchronous cancellation dial on unexpected EOF. No authentication stub.
	backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "08004", Message: tlsTransportStop})
	result.err = backend.Flush()
	return result
}

func newTransportTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "fleetdiff ephemeral TLS test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return ca, key, path
}

func newTransportTestCertificate(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, hostname string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, DNSNames: []string{hostname},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
