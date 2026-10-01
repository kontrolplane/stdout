// Package client builds a Loki client from the connection options logcli takes.
package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kontrolplane/stdout/pkg/loki"
)

// DefaultAddr is where logcli looks for Loki without an address.
const DefaultAddr = "http://localhost:3100"

// Options are the connection settings, named after the logcli flags and environment variables.
type Options struct {
	Addr            string
	Username        string
	Password        string
	OrgID           string
	BearerToken     string
	BearerTokenFile string
	AuthHeader      string
	CACert          string
	Cert            string
	Key             string
	TLSSkipVerify   bool
	ServerName      string
}

// OptionsFromEnv reads the environment variables logcli reads.
func OptionsFromEnv() Options {
	skip, _ := strconv.ParseBool(os.Getenv("LOKI_TLS_SKIP_VERIFY"))
	return Options{
		Addr:            env("LOKI_ADDR", DefaultAddr),
		Username:        os.Getenv("LOKI_USERNAME"),
		Password:        os.Getenv("LOKI_PASSWORD"),
		OrgID:           os.Getenv("LOKI_ORG_ID"),
		BearerToken:     os.Getenv("LOKI_BEARER_TOKEN"),
		BearerTokenFile: os.Getenv("LOKI_BEARER_TOKEN_FILE"),
		AuthHeader:      env("LOKI_AUTH_HEADER", "Authorization"),
		CACert:          os.Getenv("LOKI_CA_CERT_PATH"),
		Cert:            os.Getenv("LOKI_CLIENT_CERT_PATH"),
		Key:             os.Getenv("LOKI_CLIENT_KEY_PATH"),
		TLSSkipVerify:   skip,
		ServerName:      os.Getenv("LOKI_SERVER_NAME"),
	}
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// Info describes the connection for the header.
type Info struct {
	Addr   string
	Tenant string
	Auth   string // how requests authenticate: basic as a user, a bearer token, or none
}

// Validate reports options that cannot work together.
func (o Options) Validate() error {
	switch {
	case o.BearerToken != "" && o.BearerTokenFile != "":
		return errors.New("--bearer-token and --bearer-token-file cannot be used together")
	case (o.BearerToken != "" || o.BearerTokenFile != "") && (o.Username != "" || o.Password != ""):
		return errors.New("a bearer token cannot be used together with a username and password")
	case (o.Cert == "") != (o.Key == ""):
		return errors.New("--cert and --key must be used together")
	}
	return nil
}

// New builds the client the options describe.
func New(o Options, userAgent string) (*loki.Client, Info, error) {
	if err := o.Validate(); err != nil {
		return nil, Info{}, err
	}
	header := http.Header{}
	header.Set("User-Agent", userAgent)
	info := Info{Tenant: o.OrgID, Auth: "none"}
	if o.OrgID != "" {
		header.Set("X-Scope-OrgID", o.OrgID)
	}
	authHeader := o.AuthHeader
	if authHeader == "" {
		authHeader = "Authorization"
	}
	token := o.BearerToken
	if o.BearerTokenFile != "" {
		b, err := os.ReadFile(o.BearerTokenFile)
		if err != nil {
			return nil, Info{}, fmt.Errorf("reading the bearer token: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	switch {
	case token != "":
		header.Set(authHeader, "Bearer "+token)
		info.Auth = "bearer token"
	case o.Username != "" || o.Password != "":
		req := http.Request{Header: http.Header{}}
		req.SetBasicAuth(o.Username, o.Password)
		header.Set(authHeader, req.Header.Get("Authorization"))
		info.Auth = "basic " + o.Username
	}

	tlsConfig, err := o.tlsConfig()
	if err != nil {
		return nil, Info{}, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	httpClient := &http.Client{Transport: transport}

	c, err := loki.NewClient(o.Addr, httpClient, header)
	if err != nil {
		return nil, Info{}, err
	}
	info.Addr = c.Addr()
	return c, info, nil
}

func (o Options) tlsConfig() (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: o.TLSSkipVerify, //nolint:gosec // asked for with --tls-skip-verify
		ServerName:         o.ServerName,
	}
	if o.CACert != "" {
		pem, err := os.ReadFile(o.CACert)
		if err != nil {
			return nil, fmt.Errorf("reading the ca certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", o.CACert)
		}
		cfg.RootCAs = pool
	}
	if o.Cert != "" {
		cert, err := tls.LoadX509KeyPair(o.Cert, o.Key)
		if err != nil {
			return nil, fmt.Errorf("loading the client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// Check makes sure the server answers and accepts the credentials, by listing the labels of the
// last minute, which every Loki and gateway serves.
func Check(ctx context.Context, c *loki.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	now := time.Now()
	_, err := c.Labels(ctx, "", now.Add(-time.Minute), now)
	return err
}
