// Copyright 2023 Gabriel Adrian Samfira
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package httpsrv

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	_ "expvar"         // Register the expvar handlers
	_ "net/http/pprof" // Register the pprof handlers

	"github.com/gabriel-samfira/localshow/apiserver/controllers"
	"github.com/gabriel-samfira/localshow/apiserver/router"
	"github.com/gabriel-samfira/localshow/config"
	"github.com/gabriel-samfira/localshow/params"
)

// newBaseTransport returns an http.Transport with sensible defaults for
// reaching a tunneled backend. Callers must choose the wire protocol
// explicitly: because a custom DialContext is set, net/http does not
// enable HTTP/2 on its own (see http.Transport.ForceAttemptHTTP2).
func newBaseTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// isGRPCRequest reports whether r is a native gRPC request. gRPC is only
// defined over HTTP/2 and uses the "application/grpc" media type, optionally
// suffixed with the message encoding (for example "application/grpc+proto").
//
// gRPC-Web ("application/grpc-web", "application/grpc-web-text", ...)
// deliberately does not match: it is designed to work over HTTP/1.1 and is
// served by HTTP/1.1 capable backends.
func isGRPCRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mediaType == "application/grpc" || strings.HasPrefix(mediaType, "application/grpc+")
}

// backendTransport selects the wire protocol used to reach a tunneled
// backend.
//
// Backends reached over https negotiate the protocol themselves through
// TLS ALPN, so a single transport with HTTP/2 enabled serves both gRPC
// (h2) and regular HTTPS (http/1.1) services.
//
// Plaintext backends have no negotiation mechanism, so the choice is made
// per request: native gRPC, which is only defined over HTTP/2, is sent
// using unencrypted HTTP/2 with prior knowledge (h2c). Everything else,
// including WebSocket upgrades, Server-Sent Events and gRPC-Web, uses
// HTTP/1.1, which is what the vast majority of local development servers
// speak.
type backendTransport struct {
	// h1 speaks HTTP/1.1, and additionally HTTP/2 via ALPN for https
	// backends.
	h1 http.RoundTripper
	// h2c speaks unencrypted HTTP/2 with prior knowledge. It is nil for
	// https backends, where ALPN takes care of protocol selection.
	h2c http.RoundTripper
}

func (t *backendTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.h2c != nil && isGRPCRequest(r) {
		return t.h2c.RoundTrip(r)
	}
	return t.h1.RoundTrip(r)
}

// newBackendTransport returns the transport used to reach a backend that
// is served over the given URL scheme ("http" or "https").
func newBackendTransport(scheme string) http.RoundTripper {
	if scheme == "https" {
		t := newBaseTransport()
		// The backend certificate cannot be verified: it is whatever the
		// user runs locally, reached over the SSH tunnel. Skipping
		// verification is safe because the tunnel itself is authenticated
		// and encrypted.
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		// A custom DialContext or TLSClientConfig disables HTTP/2 unless
		// it is explicitly requested. With it enabled, ALPN lets the
		// backend pick h2 (gRPC) or http/1.1.
		t.ForceAttemptHTTP2 = true
		return &backendTransport{h1: t}
	}

	h2c := newBaseTransport()
	// Unencrypted HTTP/2 is only used for http:// URLs when HTTP/1 is not
	// part of the protocol set.
	h2c.Protocols = new(http.Protocols)
	h2c.Protocols.SetUnencryptedHTTP2(true)

	return &backendTransport{
		h1:  newBaseTransport(),
		h2c: h2c,
	}
}

func NewHTTPServer(ctx context.Context, cfg *config.Config, tunnelEvents chan params.TunnelEvent, controller *controllers.APIController) (*HTTPServer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.HTTPServer.BindAddress())
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", cfg.HTTPServer.BindAddress(), err)
	}

	var tlsListener net.Listener
	if cfg.HTTPServer.UseTLS {
		tlsListener, err = net.Listen("tcp", cfg.HTTPServer.TLSBindAddress())
		if err != nil {
			return nil, fmt.Errorf("failed to listen on %s: %w", cfg.HTTPServer.TLSBindAddress(), err)
		}
	}

	var debugListener net.Listener
	if cfg.DebugServer.Enabled {
		debugListener, err = net.Listen("tcp", cfg.DebugServer.BindAddressString())
		if err != nil {
			return nil, fmt.Errorf("failed to listen on %s: %w", cfg.DebugServer.BindAddressString(), err)
		}
	}

	router := router.NewAPIRouter(controller)

	return &HTTPServer{
		listener:         listener,
		tlsListener:      tlsListener,
		debugListener:    debugListener,
		cfg:              cfg,
		tunEvents:        tunnelEvents,
		ctx:              ctx,
		rootServerRouter: router,
	}, nil
}

type proxyTarget struct {
	remote    *httputil.ReverseProxy
	subdomain string
	bindAddr  string
	bindPort  uint32
	msgChan   chan params.NotifyMessage
	errChan   chan error
}

func (p *proxyTarget) logRequest(r *http.Request) {
	if p.msgChan == nil {
		return
	}
	clientIP := r.RemoteAddr
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		clientIP = ip
	}
	tm := time.Now().UTC()
	logMsg := fmt.Sprintf("%s - - %s \"%s %s %s\" %s %dus", clientIP,
		tm.Format("02/Jan/2006:15:04:05 -0700"),
		r.Method,
		r.URL.Path,
		r.Proto,
		r.UserAgent(),
		time.Since(tm))
	// Never let logging stall a request. The channel is drained by the
	// SSH session that owns the tunnel; if that side stops consuming
	// (for example while the session is being torn down) the log line
	// is dropped rather than blocking the proxied request.
	select {
	case p.msgChan <- params.NotifyMessage{
		MessageType: params.NotifyMessageLog,
		Payload:     []byte(logMsg),
	}:
	default:
	}
}

type HTTPServer struct {
	listener         net.Listener
	tlsListener      net.Listener
	debugListener    net.Listener
	cfg              *config.Config
	tunEvents        chan params.TunnelEvent
	ctx              context.Context
	rootServerRouter http.Handler

	vhosts sync.Map // map[string]*proxyTarget

	srv      *http.Server
	debugSrv *http.Server
}

func (h *HTTPServer) tunnelSuccessURLs(subdomain string) ([]byte, error) {
	urls := params.URLs{}
	dom := fmt.Sprintf("%s.%s", subdomain, h.cfg.HTTPServer.DomainName)

	httpPort := h.cfg.HTTPServer.EffectivePort()
	httpTunnel := fmt.Sprintf("http://%s", dom)
	if httpPort != 80 {
		httpTunnel = fmt.Sprintf("%s:%d", httpTunnel, httpPort)
	}
	urls.HTTP = httpTunnel

	if h.cfg.HTTPServer.UseTLS {
		tlsPort := h.cfg.HTTPServer.EffectiveTLSPort()
		httpsTunnel := fmt.Sprintf("https://%s", dom)
		if tlsPort != 443 {
			httpsTunnel = fmt.Sprintf("%s:%d", httpsTunnel, tlsPort)
		}
		urls.HTTPS = httpsTunnel
	}

	return json.Marshal(urls)
}

var portMap = map[uint32]string{
	80:  "http",
	443: "https",
}

func (h *HTTPServer) registerTunnel(event params.TunnelEvent) (err error) {
	defer func() {
		if err != nil {
			select {
			case event.ErrorChan <- err:
			case <-time.After(5 * time.Second):
			}
		}
	}()

	if strings.Contains(event.RequestedSubdomain, ".") {
		return fmt.Errorf("invalid subdomain %s", event.RequestedSubdomain)
	}

	dom := fmt.Sprintf("%s.%s", event.RequestedSubdomain, h.cfg.HTTPServer.DomainName)
	if _, loaded := h.vhosts.Load(dom); loaded {
		return fmt.Errorf("subdomain %s already registered", event.RequestedSubdomain)
	}

	remote, err := url.Parse(fmt.Sprintf("%s://%s", portMap[event.RequestedPort], event.BindAddr))
	if err != nil {
		return fmt.Errorf("failed to parse bind address %s: %w", event.BindAddr, err)
	}

	// Use Rewrite (not the legacy Director) so hop-by-hop headers such as
	// Connection: Upgrade are forwarded correctly, enabling WebSocket and
	// other HTTP upgrade protocols.
	reverseProxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(remote)
			// SetXForwarded appends to X-Forwarded-For (IP only),
			// and sets X-Forwarded-Host and X-Forwarded-Proto from
			// the inbound request.
			pr.SetXForwarded()

			clientIP, _, splitErr := net.SplitHostPort(pr.In.RemoteAddr)
			if splitErr == nil {
				pr.Out.Header.Set("X-Real-IP", clientIP)
			}

			if pr.In.TLS != nil {
				pr.Out.Header.Set("X-Forwarded-Port", fmt.Sprintf("%d", h.cfg.HTTPServer.EffectiveTLSPort()))
			} else {
				pr.Out.Header.Set("X-Forwarded-Port", fmt.Sprintf("%d", h.cfg.HTTPServer.EffectivePort()))
			}

			// Rewrite Origin so CORS checks pass on the backend.
			origin := pr.In.Header.Get("Origin")
			if origin != "" {
				inHost := pr.In.Host
				if host, _, herr := net.SplitHostPort(inHost); herr == nil {
					inHost = host
				}
				origParsed, perr := url.Parse(origin)
				if perr == nil && origParsed.Hostname() == inHost {
					pr.Out.Header.Set("Origin", fmt.Sprintf("%s://%s", remote.Scheme, remote.Host))
				}
			}
		},
		// Leave FlushInterval at zero. The reverse proxy already flushes
		// immediately for streamed responses (unknown Content-Length,
		// which covers gRPC streams and chunked HTTP/1.1) and for
		// Server-Sent Events. A negative FlushInterval would also arm an
		// immediate flush for responses with no body at all, which races
		// with the handler finishing and turns a gRPC "trailers-only"
		// response (a single HEADERS frame with END_STREAM) into HEADERS
		// followed by an empty DATA frame. gRPC clients reject that with
		// "server closed the stream without sending trailers".
		Transport: newBackendTransport(remote.Scheme),
	}
	log.Printf("registering tunnel for %s", dom)

	urls, err := h.tunnelSuccessURLs(event.RequestedSubdomain)
	if err != nil {
		return fmt.Errorf("failed to get urls: %w", err)
	}
	event.NotifyChan <- params.NotifyMessage{
		MessageType: params.NotifyMessageURL,
		Payload:     urls,
	}
	// Register the vhost after the notify message is sent to the client. This ensures
	// that the first message that is sent through the channel is the URL message.
	h.vhosts.Store(dom, &proxyTarget{
		remote:    reverseProxy,
		subdomain: event.RequestedSubdomain,
		bindAddr:  event.BindAddr,
		bindPort:  event.RequestedPort,
		msgChan:   event.NotifyChan,
		errChan:   event.ErrorChan,
	})
	return nil
}

func (h *HTTPServer) unregisterTunnel(event params.TunnelEvent) error {
	dom := fmt.Sprintf("%s.%s", event.RequestedSubdomain, h.cfg.HTTPServer.DomainName)
	log.Printf("unregistering tunnel for %s", dom)
	if _, loaded := h.vhosts.LoadAndDelete(dom); !loaded {
		log.Printf("subdomain %s (%s) not registered", event.RequestedSubdomain, dom)
	}
	return nil
}

// extractHostname returns the hostname portion of a host or host:port string.
func extractHostname(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		// No port present; hostport is already a bare hostname.
		return hostport
	}
	return host
}

func (h *HTTPServer) handlerFunc() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostname := extractHostname(r.Host)
		if hostname == h.cfg.HTTPServer.DomainName {
			h.rootServerRouter.ServeHTTP(w, r)
			return
		}

		val, ok := h.vhosts.Load(hostname)
		if !ok {
			w.WriteHeader(http.StatusBadGateway)
			w.Write(badRequestHTML(hostname))
			return
		}
		p := val.(*proxyTarget)
		p.logRequest(r)
		// All header manipulation (X-Forwarded-*, X-Real-IP, Origin
		// rewriting) is handled inside the ReverseProxy Rewrite function.
		p.remote.ServeHTTP(w, r)
	}
}

func (h *HTTPServer) loop() {
	defer func() {
		if err := h.Stop(); err != nil {
			log.Printf("failed to stop http server: %s", err)
		}
		if h.listener != nil {
			h.listener.Close()
		}
		if h.tlsListener != nil {
			h.tlsListener.Close()
		}
		if h.debugListener != nil {
			h.debugListener.Close()
		}
	}()

	for {
		select {
		case <-h.ctx.Done():
			return
		case tunEvent, ok := <-h.tunEvents:
			if !ok {
				return
			}
			switch tunEvent.EventType {
			case params.EventTypeTunnelReady:
				if err := h.registerTunnel(tunEvent); err != nil {
					log.Printf("failed to register tunnel: %s", err)
				}
			case params.EventTypeTunnelClosed:
				if err := h.unregisterTunnel(tunEvent); err != nil {
					log.Printf("failed to unregister tunnel: %s", err)
				}
			default:
				log.Printf("unknown event type: %s", tunEvent.EventType)
			}
		}
	}
}

// newServer returns the http.Server that fronts all tunnels. The same
// server instance serves both the plaintext and the TLS listener.
func (h *HTTPServer) newServer() *http.Server {
	// Accept HTTP/1.1, HTTP/2 over TLS (negotiated through ALPN) and
	// unencrypted HTTP/2 with prior knowledge on the plaintext listener.
	// The latter is what gRPC clients use when connecting to an insecure
	// http:// endpoint. WebSocket clients keep using HTTP/1.1 because
	// extended CONNECT (RFC 8441) is not advertised.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)

	return &http.Server{
		Handler:           h.handlerFunc(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Protocols:         &protocols,
	}
}

func (h *HTTPServer) startReverseProxy() error {
	srv := h.newServer()
	h.srv = srv

	go func() {
		if err := srv.Serve(h.listener); err != http.ErrServerClosed {
			log.Printf("failed to serve on http: %s", err)
		}
	}()

	go func() {
		if h.cfg.HTTPServer.UseTLS && h.tlsListener != nil {
			if err := srv.ServeTLS(h.tlsListener, h.cfg.HTTPServer.TLSConfig.CRT, h.cfg.HTTPServer.TLSConfig.Key); err != http.ErrServerClosed {
				log.Printf("failed to serve on HTTPS: %s", err)
			}
		}
	}()

	go h.loop()
	return nil
}

func (h *HTTPServer) startDebugServer() error {
	srv := &http.Server{
		Handler: http.DefaultServeMux,
	}
	h.debugSrv = srv

	go func() {
		if err := srv.Serve(h.debugListener); err != http.ErrServerClosed {
			log.Printf("failed to serve on http: %s", err)
		}
	}()
	return nil
}

func (h *HTTPServer) Start() error {
	if err := h.startReverseProxy(); err != nil {
		return fmt.Errorf("failed to start reverse proxy: %w", err)
	}

	if h.cfg.DebugServer.Enabled {
		if err := h.startDebugServer(); err != nil {
			return fmt.Errorf("failed to start debug server: %w", err)
		}
	}

	return nil
}

func (h *HTTPServer) Stop() error {
	if h.srv == nil {
		return nil
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer shutdownCancel()
	if err := h.srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("failed to shutdown http server: %w", err)
	}

	if h.debugSrv != nil {
		if err := h.debugSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shutdown debug server: %w", err)
		}
	}

	return nil
}
