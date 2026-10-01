package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kontrolplane/stdout/pkg/client"
	"github.com/kontrolplane/stdout/pkg/loki"
	tui "github.com/kontrolplane/stdout/pkg/tui"
	"github.com/kontrolplane/stdout/pkg/tui/styles"
)

var (
	projectName = "kontrolplane"
	programName = "stdout"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// secretFlags hold credentials, so their defaults, which come from the environment, are not
// printed in the usage.
var secretFlags = map[string]bool{"password": true, "bearer-token": true}

// usage prints the help, with the flags spelled with two dashes as the readme and logcli do.
func usage(w io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(w, `%[1]s is a terminal user interface for following Loki logs.

usage: %[1]s [flags] [query]

Without a query, %[1]s starts on a picker of labels and their values to build one.

examples:
  %[1]s
  %[1]s '{app="api"}'
  %[1]s --addr https://logs.example.com --org-id team-a '{namespace="prod"} |= "error"'

flags:
`, programName)
	fs.VisitAll(func(f *flag.Flag) {
		name := "--" + f.Name
		if kind, _ := flag.UnquoteUsage(f); kind != "" {
			name += " " + kind
		}
		line := "  " + name
		if f.DefValue != "" && f.DefValue != "false" && !secretFlags[f.Name] {
			def := f.DefValue
			if kind, _ := flag.UnquoteUsage(f); kind == "string" {
				def = strconv.Quote(def)
			}
			_, _ = fmt.Fprintf(w, "%-32s %s (default %s)\n", line, f.Usage, def)
			return
		}
		_, _ = fmt.Fprintf(w, "%-32s %s\n", line, f.Usage)
	})
}

func Execute(version, commit, date string) {
	opts := client.OptionsFromEnv()
	config := tui.Config{Since: time.Hour, Limit: 100, Buffer: 10000}

	fs := flag.NewFlagSet(programName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.Addr, "addr", opts.Addr, "loki server address (env LOKI_ADDR)")
	fs.StringVar(&opts.Username, "username", opts.Username, "basic auth username (env LOKI_USERNAME)")
	fs.StringVar(&opts.Password, "password", opts.Password, "basic auth password (env LOKI_PASSWORD)")
	fs.StringVar(&opts.OrgID, "org-id", opts.OrgID, "tenant, sent as X-Scope-OrgID (env LOKI_ORG_ID)")
	fs.StringVar(&opts.BearerToken, "bearer-token", opts.BearerToken, "bearer token (env LOKI_BEARER_TOKEN)")
	fs.StringVar(&opts.BearerTokenFile, "bearer-token-file", opts.BearerTokenFile, "file holding the bearer token (env LOKI_BEARER_TOKEN_FILE)")
	fs.StringVar(&opts.AuthHeader, "auth-header", opts.AuthHeader, "header the credentials are sent in (env LOKI_AUTH_HEADER)")
	fs.StringVar(&opts.CACert, "ca-cert", opts.CACert, "certificate authority to verify the server with (env LOKI_CA_CERT_PATH)")
	fs.StringVar(&opts.Cert, "cert", opts.Cert, "client tls certificate, requires --key (env LOKI_CLIENT_CERT_PATH)")
	fs.StringVar(&opts.Key, "key", opts.Key, "client tls key, requires --cert (env LOKI_CLIENT_KEY_PATH)")
	fs.BoolVar(&opts.TLSSkipVerify, "tls-skip-verify", opts.TLSSkipVerify, "do not verify the server certificate (env LOKI_TLS_SKIP_VERIFY)")
	fs.DurationVar(&config.Since, "since", config.Since, "how far back labels, the first lines and each page of older lines reach")
	fs.IntVar(&config.Limit, "limit", config.Limit, "lines the tail starts with")
	fs.IntVar(&config.Buffer, "buffer", config.Buffer, "lines kept in memory, the oldest are dropped past it")
	theme := fs.String("theme", "auto", "colour theme: auto follows the terminal, dark or light also paint the background")
	debug := fs.Bool("debug", false, "write debug logs to debug.log")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(os.Stdout, fs)
			return
		}
		fmt.Fprintf(os.Stderr, "%s (see %s --help)\n", styles.Clean(err.Error()), programName)
		os.Exit(2)
	}

	if fs.NArg() > 1 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q, %s takes one query, quote it (see %s --help)\n", fs.Arg(1), programName, programName)
		os.Exit(2)
	}
	config.Query = strings.TrimSpace(fs.Arg(0))

	if *showVersion {
		fmt.Printf("%s %s (commit %s, built %s)\n", programName, version, commit, date)
		return
	}

	switch {
	case config.Since <= 0:
		fail("--since must be positive")
	case config.Limit < 0:
		fail("--limit cannot be negative")
	case config.Buffer < 100:
		fail("--buffer must be at least 100")
	}

	fs.Visit(func(f *flag.Flag) {
		if secretFlags[f.Name] {
			fmt.Fprintf(os.Stderr, "warning: --%s is visible to other users in the process list, prefer its environment variable\n", f.Name)
		}
	})

	switch *theme {
	case "dark":
		styles.Use(true)
		styles.Paint = true
	case "light":
		styles.Use(false)
		styles.Paint = true
	case "auto":
		styles.Use(lipgloss.HasDarkBackground(os.Stdin, os.Stdout))
	default:
		fmt.Fprintf(os.Stderr, "unknown theme %q, use auto, dark or light\n", styles.Clean(*theme))
		os.Exit(2)
	}

	log.SetOutput(io.Discard)
	if *debug {
		f, err := tea.LogToFile("debug.log", "debug")
		if err != nil {
			fail("could not open debug.log: %s", styles.Clean(err.Error()))
		}
		defer func() { _ = f.Close() }()
	}

	c, info, err := client.New(opts, fmt.Sprintf("%s/%s %s", projectName, programName, version))
	if err != nil {
		fail("%s", styles.Clean(err.Error()))
	}
	if err := client.Check(context.Background(), c); err != nil {
		reason := err
		// The request url says nothing the address does not.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			reason = urlErr.Err
		}
		msg := fmt.Sprintf("error connecting to loki at %s: %s", info.Addr, styles.Clean(loki.Describe(reason)))
		var netErr net.Error
		var opErr *net.OpError
		if errors.As(err, &opErr) || errors.As(err, &netErr) {
			msg += fmt.Sprintf("\nis loki running? pick one with --addr or LOKI_ADDR (see %s --help)", programName)
		}
		fail("%s", msg)
	}

	model := tui.NewModel(projectName, programName, c, info, config)
	if _, err := tea.NewProgram(model).Run(); err != nil {
		fail("error running program: %s", styles.Clean(err.Error()))
	}
}
