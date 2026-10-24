package mcp

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func Healthy() error {
	addr := HTTPAddr()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s=%q: %w", HTTPAddrEnv, addr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + HTTPPath

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(os.Getenv(HTTPTokenEnv)))

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("the server does not answer at %s: %w", url, err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode >= 500 {
		return fmt.Errorf("the server at %s answers %s", url, resp.Status)
	}
	return nil
}
