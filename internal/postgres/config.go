// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package postgres is the explicit network boundary for read-only LiteLLM exports.
package postgres

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/localfile"
)

const credentialEnv = "FLEETDIFF_LITELLM_DATABASE_URL"

var errCredentials = exportdata.ErrCredentials
var errURL = exportdata.ErrConfiguration
var configMu sync.Mutex

func credentials(path string) (string, error) {
	value, set := os.LookupEnv(credentialEnv)
	if path != "" {
		if set {
			return "", errCredentials
		}
		root, name, err := openParent(path)
		if err != nil {
			return "", errCredentials
		}
		defer root.Close()
		data, err := readCredentials(root, name)
		if err != nil {
			return "", errCredentials
		}
		value = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if len(value) == 0 || len(value) > 64<<10 {
		return "", errCredentials
	}
	return value, nil
}

func readCredentials(root *os.Root, name string) ([]byte, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errCredentials
	}
	defer f.Close()
	return readCredentialFile(f)
}

func readCredentialFile(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil || !owned(info) || info.Mode() != 0600 || info.Size() > 64<<10 {
		return nil, errCredentials
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errCredentials
	}
	after, err := f.Stat()
	if err != nil || !owned(after) || after.Mode() != 0600 || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errCredentials
	}
	return data, nil
}

func connectionConfig(raw string) (*pgconn.Config, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Opaque != "" || u.Fragment != "" || u.User == nil || u.User.Username() == "" {
		return nil, errURL
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errURL
	}
	for key, values := range q {
		if len(values) != 1 || key != "sslmode" && key != "sslrootcert" && key != "host" {
			return nil, errURL
		}
	}
	host := u.Hostname()
	if socket := q.Get("host"); socket != "" {
		if host != "" || u.Port() != "" || !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
			return nil, errURL
		}
		host = socket
	}
	db := strings.TrimPrefix(u.Path, "/")
	password, _ := u.User.Password()
	for _, field := range []string{host, db, u.User.Username(), password} {
		if strings.IndexFunc(field, func(r rune) bool { return r == 0 || r == '\r' || r == '\n' }) >= 0 {
			return nil, errURL
		}
	}
	if host == "" || db == "" || strings.ContainsAny(db, "/\\") || strings.Contains(host, ",") || strings.Contains(host, "%") {
		return nil, errURL
	}
	port := uint64(5432)
	if u.Port() != "" {
		port, err = strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return nil, errURL
		}
	}
	local := filepath.IsAbs(host) || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
	mode := q.Get("sslmode")
	if mode != "verify-full" && (!local || mode != "disable") {
		return nil, exportdata.ErrTransport
	}
	if filepath.IsAbs(host) && mode != "disable" {
		return nil, errURL
	}
	c, err := neutralConfig()
	if err != nil {
		return nil, errors.New("cannot initialize database configuration")
	}
	c.Host, c.Port, c.Database, c.User, c.Password = host, uint16(port), db, u.User.Username(), password
	c.Fallbacks = nil
	c.ConnectTimeout = 10 * time.Second
	c.MaxProtocolMessageBodyLen = (64 << 10) + 256
	c.RuntimeParams = map[string]string{"application_name": "fleetdiff", "client_encoding": "UTF8", "default_transaction_read_only": "on"}
	if mode == "verify-full" {
		c.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
		if ca := q.Get("sslrootcert"); ca != "" {
			root, name, err := openParent(ca)
			if err != nil {
				return nil, errors.New("cannot read configured database CA file")
			}
			pem, err := localfile.Read(root, name, 1<<20)
			root.Close()
			pool := x509.NewCertPool()
			if err != nil || !pool.AppendCertsFromPEM(pem) {
				return nil, errors.New("invalid database CA file")
			}
			c.TLSConfig.RootCAs = pool
		}
	} else if q.Get("sslrootcert") != "" {
		return nil, errURL
	}
	return c, nil
}

// pgconn requires its parser's initialized Config. Parse a constant template,
// not the supplied URL, with all inherited PG* settings temporarily removed.
// Explicit empty certificate paths and a nonempty placeholder password suppress
// home-file defaults. Replace every destination/credential before connecting.
func neutralConfig() (*pgconn.Config, error) {
	configMu.Lock()
	defer configMu.Unlock()
	previous := map[string]string{}
	defer func() {
		for key, value := range previous {
			_ = os.Setenv(key, value)
		}
	}()
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") {
			previous[key] = value
			if err := os.Unsetenv(key); err != nil {
				return nil, errors.New("cannot isolate database environment")
			}
		}
	}
	return pgconn.ParseConfig("host=127.0.0.1 port=5432 dbname=fleetdiff user=fleetdiff password=no-inherited-credentials sslmode=disable sslrootcert='' sslcert='' sslkey='' sslpassword='' connect_timeout=10")
}
