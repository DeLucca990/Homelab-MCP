package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Streamable HTTP, the alternative to stdio.
//
// Over stdio a client has to be able to exec the binary, which off-box means
// wrapping it in ssh and paying for a whole server process per client. Over
// HTTP one long-lived process serves every client that can reach the address,
// the .env stays on the server where the key belongs, and clients that speak
// HTTP natively (Claude Code, the MCP Inspector, mcp-remote) need no wrapper.
//
// What it costs is a listening port. Everything below exists to make that port
// safe to open: bind it to a private interface, require a token, and refuse to
// start rather than serve these tools anonymously by accident.

const (
	HTTPAddrEnv  = "HOMELAB_MCP_HTTP_ADDR"
	HTTPTokenEnv = "HOMELAB_MCP_HTTP_TOKEN"
)

const HTTPPath = "/mcp"

const (
	// Applies to the headers only. A response can be an SSE stream the client
	// holds open, so there is deliberately no write deadline — one would cut
	// every long-lived stream at the same age.
	readHeaderTimeout = 10 * time.Second

	// Long enough for an in-flight tool call to finish, short enough that a
	// systemd restart does not hang on a client that stopped listening.
	shutdownGrace = 10 * time.Second
)

// HTTPAddr reports the configured listen address, empty when the server should
// speak stdio.
func HTTPAddr() string {
	return strings.TrimSpace(os.Getenv(HTTPAddrEnv))
}

// ServeHTTP runs the server over Streamable HTTP until ctx is cancelled.
func ServeHTTP(ctx context.Context, server *sdk.Server, addr string) error {
	token, err := httpToken()
	if err != nil {
		return err
	}

	handler := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{
			Stateless:                  true,
			DisableLocalhostProtection: true,
		},
	)

	mux := http.NewServeMux()
	mux.Handle(HTTPPath, requireBearer(token, handler))

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s (%s): %w", addr, HTTPAddrEnv, err)
	}

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          log.Default(),
	}

	log.Printf("MCP server running on transport streamable http at http://%s%s ", listener.Addr(), HTTPPath)
	warnIfWideOpen(listener.Addr())

	// Shutdown runs in its own goroutine because ListenAndServe owns this one
	// until it returns, and it only returns once Shutdown has been called.
	shutdown := make(chan error, 1)
	go func() {
		<-ctx.Done()
		grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		shutdown <- httpServer.Shutdown(grace)
	}()

	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	if err := <-shutdown; err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}

// httpToken resolves the credential the port cannot open without.
func httpToken() (string, error) {
	token := strings.TrimSpace(os.Getenv(HTTPTokenEnv))

	if token == "" {
		return "", fmt.Errorf(
			"refusing to serve over HTTP without authentication: set %s to a random "+
				"secret (`openssl rand -hex 32`) and send it as "+
				"`Authorization: Bearer <token>` from every client",
			HTTPTokenEnv)
	}

	return token, nil
}

// requireBearer authenticates every request. Nothing reaches the MCP handler
// without the token — not tools/list, not initialize, not a ping.
func requireBearer(token string, next http.Handler) http.Handler {
	expected := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r.Header.Get("Authorization"), expected) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="homelab-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			log.Printf("http: rejected an unauthenticated request from %s", r.RemoteAddr)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func authorized(header string, expected []byte) bool {
	scheme, presented, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(presented)), expected) == 1
}

// warnIfWideOpen says out loud what an address means, at the only moment anyone
// is reading. The token makes this survivable rather than a breach, but a bare
// ":3000" is usually an operator who read it as "localhost" and has just put
// the endpoint on their LAN as well as their tailnet.
func warnIfWideOpen(addr net.Addr) {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
		return
	}

	log.Printf("note: listening on every interface, not just one. Only the token "+
		"stands between the tools and anything that can route here — bind a single "+
		"address in %s (e.g. 127.0.0.1:3000, or this host's tailscale address) unless "+
		"that is what you meant", HTTPAddrEnv)
}
