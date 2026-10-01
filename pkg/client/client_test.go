package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestOptionsFromEnv(t *testing.T) {
	t.Setenv("LOKI_ADDR", "https://logs.example.com")
	t.Setenv("LOKI_ORG_ID", "team-a")
	t.Setenv("LOKI_TLS_SKIP_VERIFY", "true")
	t.Setenv("LOKI_AUTH_HEADER", "")
	o := OptionsFromEnv()
	if o.Addr != "https://logs.example.com" || o.OrgID != "team-a" || !o.TLSSkipVerify || o.AuthHeader != "Authorization" {
		t.Errorf("OptionsFromEnv() = %+v", o)
	}
	t.Setenv("LOKI_ADDR", "")
	if got := OptionsFromEnv().Addr; got != DefaultAddr {
		t.Errorf("default addr = %q", got)
	}
}

func TestValidate(t *testing.T) {
	bad := []Options{
		{BearerToken: "a", BearerTokenFile: "b"},
		{BearerToken: "a", Username: "u"},
		{Cert: "c.pem"},
		{Key: "k.pem"},
	}
	for _, o := range bad {
		if o.Validate() == nil {
			t.Errorf("%+v: want an error", o)
		}
	}
	if err := (Options{Username: "u", Password: "p"}).Validate(); err != nil {
		t.Error(err)
	}
}

func TestNewSendsCredentials(t *testing.T) {
	tests := []struct {
		name     string
		opts     Options
		header   string
		want     string
		wantAuth string
	}{
		{"basic", Options{Username: "u", Password: "p"}, "Authorization", "Basic dTpw", "basic u"},
		{"bearer", Options{BearerToken: "t0k"}, "Authorization", "Bearer t0k", "bearer token"},
		{"custom header", Options{BearerToken: "t0k", AuthHeader: "X-Auth"}, "X-Auth", "Bearer t0k", "bearer token"},
		{"none", Options{}, "Authorization", "", "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got, tenant, agent string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, tenant, agent = r.Header.Get(tt.header), r.Header.Get("X-Scope-OrgID"), r.UserAgent()
				_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
			}))
			defer srv.Close()
			tt.opts.Addr = srv.URL
			tt.opts.OrgID = "team-a"
			c, info, err := New(tt.opts, "kontrolplane/stdout")
			if err != nil {
				t.Fatal(err)
			}
			if err := Check(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if got != tt.want || tenant != "team-a" || agent != "kontrolplane/stdout" {
				t.Errorf("header %q, tenant %q, agent %q", got, tenant, agent)
			}
			if info.Auth != tt.wantAuth || info.Tenant != "team-a" || info.Addr != srv.URL {
				t.Errorf("info = %+v", info)
			}
		})
	}
}

func TestBearerTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	}))
	defer srv.Close()
	c, _, err := New(Options{Addr: srv.URL, BearerTokenFile: path}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer from-file" {
		t.Errorf("Authorization = %q", got)
	}
	if _, _, err := New(Options{Addr: srv.URL, BearerTokenFile: path + ".missing"}, "test"); err == nil {
		t.Error("want an error for a missing token file")
	}
}
