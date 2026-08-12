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

// Streamable HTTP, the only transport this server speaks.
//
// One long-lived process serves every client that can reach the address, the
// .env stays on the machine being monitored, and nothing has to be able to exec
// a binary over ssh to talk to it. The cost is a listening port, and everything
// below exists to make that port safe to open: bind it to a private interface,
// require a token, and refuse to start rather than serve these tools
// anonymously by accident.

const (
	HTTPAddrEnv  = "HOMELAB_MCP_HTTP_ADDR"
	HTTPTokenEnv = "HOMELAB_MCP_HTTP_TOKEN"
)

const HTTPPath = "/mcp"

const (
	readHeaderTimeout = 10 * time.Second
	shutdownGrace     = 10 * time.Second
)

// HTTPAddr reports the configured listen address.
func HTTPAddr() string {
	return strings.TrimSpace(os.Getenv(HTTPAddrEnv))
}

// ServeHTTP runs the server over Streamable HTTP until ctx is cancelled.
func ServeHTTP(ctx context.Context, server *sdk.Server, addr string) error {
	if addr == "" {
		return fmt.Errorf(
			"%s is not set: it is the address this server listens on, written as "+
				"host:port. Make the host explicit — this machine's tailscale address to "+
				"serve the tailnet, or 127.0.0.1 behind a reverse proxy. A bare \":3000\" "+
				"binds every interface, including the one facing your LAN",
			HTTPAddrEnv)
	}

	token := strings.TrimSpace(os.Getenv(HTTPTokenEnv))
	if token == "" {
		return fmt.Errorf(
			"refusing to serve over HTTP without authentication: set %s to a random "+
				"secret (`openssl rand -hex 32`) and send it as "+
				"`Authorization: Bearer <token>` from every client",
			HTTPTokenEnv)
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

// requireBearer authenticates every request. Nothing reaches the MCP handler
// without the token — not tools/list, not initialize, not a ping.
func requireBearer(token string, next http.Handler) http.Handler {
	expected := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, presented, found := strings.Cut(
			strings.TrimSpace(r.Header.Get("Authorization")), " ")

		ok := found && strings.EqualFold(scheme, "Bearer") &&
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(presented)), expected) == 1

		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="homelab-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			log.Printf("http: rejected an unauthenticated request from %s", r.RemoteAddr)
			return
		}

		next.ServeHTTP(w, r)
	})
}
