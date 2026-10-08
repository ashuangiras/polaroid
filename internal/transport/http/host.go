package http

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RequireLoopbackHost wraps h so that it only serves requests whose Host
// header names a loopback address: localhost, 127.0.0.0/8 or ::1. Use it when
// the server listens on a loopback address, so a web page cannot reach the
// API through DNS rebinding.
func RequireLoopbackHost(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, errorDetail{Code: "forbidden", Message: "the Host header must name a loopback address"})
			return
		}
		h.ServeHTTP(w, r)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	return err == nil && addr.IsLoopback()
}
