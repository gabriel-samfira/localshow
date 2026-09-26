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
	"bufio"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabriel-samfira/localshow/config"
	"github.com/gabriel-samfira/localshow/params"
)

const (
	testDomain    = "localshow.test"
	testSubdomain = "app"
	testHost      = testSubdomain + "." + testDomain
)

// newTestProxy returns an HTTPServer with a single tunnel registered for
// testHost, pointing at backendAddr. requestedPort mirrors the port the SSH
// client asked to forward and selects the backend scheme: 80 for http, 443
// for https.
func newTestProxy(t *testing.T, backendAddr string, requestedPort uint32) *HTTPServer {
	t.Helper()
	h := &HTTPServer{
		cfg: &config.Config{
			HTTPServer: config.HTTPServer{
				BindAddr:    "127.0.0.1",
				BindPort:    80,
				TLSBindPort: 443,
				UseTLS:      true,
				DomainName:  testDomain,
			},
		},
	}
	// Drain notifications the way the owning SSH session does.
	notify := make(chan params.NotifyMessage, 10)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-notify:
			case <-done:
				return
			}
		}
	}()
	t.Cleanup(func() { close(done) })
	err := h.registerTunnel(params.TunnelEvent{
		EventType:          params.EventTypeTunnelReady,
		NotifyChan:         notify,
		ErrorChan:          make(chan error, 10),
		BindAddr:           backendAddr,
		RequestedPort:      requestedPort,
		RequestedSubdomain: testSubdomain,
	})
	if err != nil {
		t.Fatalf("failed to register tunnel: %s", err)
	}
	return h
}

// startFront serves the production http.Server (see newServer) on a local
// listener, either plaintext or TLS, mirroring the two localshow listeners.
func startFront(t *testing.T, h *HTTPServer, useTLS bool) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	ts.Config = h.newServer()
	if useTLS {
		ts.EnableHTTP2 = true
		// The same ALPN protocols ServeTLS advertises in production.
		ts.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
		ts.StartTLS()
	} else {
		ts.Start()
	}
	t.Cleanup(ts.Close)
	return ts
}

// startBackend serves handler the way a tunneled user service would:
// plaintext servers accept HTTP/1.1 and h2c, TLS servers negotiate h2 or
// http/1.1 through ALPN.
func startBackend(t *testing.T, handler http.Handler, useTLS bool) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(handler)
	if useTLS {
		ts.EnableHTTP2 = true
		ts.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
		ts.StartTLS()
	} else {
		var protocols http.Protocols
		protocols.SetHTTP1(true)
		protocols.SetUnencryptedHTTP2(true)
		ts.Config.Protocols = &protocols
		ts.Start()
	}
	t.Cleanup(ts.Close)
	return ts
}

// h2Client returns an HTTP/2 client for the front server: prior knowledge
// h2c for plaintext, ALPN negotiated h2 for TLS. This is how gRPC clients
// connect.
func h2Client(t *testing.T, front *httptest.Server, useTLS bool) *http.Client {
	t.Helper()
	if useTLS {
		// Trusts the test certificate and has ForceAttemptHTTP2 set.
		return front.Client()
	}
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	tr := &http.Transport{Protocols: &protocols}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr}
}

// dialFront opens a raw connection to the front server, as a WebSocket
// client would.
func dialFront(t *testing.T, front *httptest.Server, useTLS bool) net.Conn {
	t.Helper()
	addr := front.Listener.Addr().String()
	var conn net.Conn
	var err error
	if useTLS {
		pool := x509.NewCertPool()
		pool.AddCert(front.Certificate())
		conn, err = tls.Dial("tcp", addr, &tls.Config{
			RootCAs:    pool,
			NextProtos: []string{"http/1.1"},
		})
	} else {
		conn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		t.Fatalf("failed to dial front server: %s", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("failed to set deadline: %s", err)
	}
	return conn
}

// grpcEchoHandler mimics a gRPC server on the wire without depending on a
// gRPC implementation: it insists on HTTP/2 and the TE: trailers header,
// echoes every chunk of the request body as soon as it arrives, and ends
// the response with gRPC style status trailers. One trailer is announced
// up front and one is not, exercising both trailer paths of the reverse
// proxy.
func grpcEchoHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "gRPC requires HTTP/2, got "+r.Proto, http.StatusHTTPVersionNotSupported)
			return
		}
		if r.Header.Get("Te") != "trailers" {
			http.Error(w, "missing TE: trailers", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			t.Errorf("backend flush failed: %s", err)
			return
		}

		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if ferr := rc.Flush(); ferr != nil {
					return
				}
			}
			if err != nil {
				break
			}
		}
		w.Header().Set("Grpc-Status", "0")
		w.Header().Set(http.TrailerPrefix+"Grpc-Message", "echo complete")
	})
}

// TestLogRequestDoesNotBlock makes sure a tunnel whose notification
// channel is not being drained cannot stall proxied requests.
func TestLogRequestDoesNotBlock(t *testing.T) {
	p := &proxyTarget{msgChan: make(chan params.NotifyMessage)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.logRequest(httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logRequest blocked on a full notification channel")
	}
}

func TestIsGRPCRequest(t *testing.T) {
	cases := map[string]bool{
		"application/grpc":           true,
		"application/grpc+proto":     true,
		"application/grpc+json":      true,
		"Application/GRPC":           true,
		"application/grpc-web":       false,
		"application/grpc-web+proto": false,
		"application/grpc-web-text":  false,
		"application/json":           false,
		"text/plain; charset=utf-8":  false,
		"":                           false,
	}
	for contentType, want := range cases {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if got := isGRPCRequest(r); got != want {
			t.Errorf("isGRPCRequest(%q) = %v, want %v", contentType, got, want)
		}
	}
}

// TestGRPCBidirectionalStreaming drives a gRPC style bidirectional stream
// through the proxy for every combination of front listener and backend
// scheme. Each message must be echoed back before the next one is sent,
// which fails if either direction is buffered or if the backend does not
// receive HTTP/2.
func TestGRPCBidirectionalStreaming(t *testing.T) {
	cases := []struct {
		name       string
		frontTLS   bool
		backendTLS bool
	}{
		{name: "h2c front to h2c backend"},
		{name: "tls front to h2c backend", frontTLS: true},
		{name: "tls front to tls backend", frontTLS: true, backendTLS: true},
		{name: "h2c front to tls backend", backendTLS: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := startBackend(t, grpcEchoHandler(t), tc.backendTLS)
			requestedPort := uint32(80)
			if tc.backendTLS {
				requestedPort = 443
			}
			h := newTestProxy(t, backend.Listener.Addr().String(), requestedPort)
			front := startFront(t, h, tc.frontTLS)
			client := h2Client(t, front, tc.frontTLS)

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()

			pr, pw := io.Pipe()
			defer pw.Close()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/echo.Service/Stream", pr)
			if err != nil {
				t.Fatalf("failed to create request: %s", err)
			}
			req.Host = testHost
			req.Header.Set("Content-Type", "application/grpc+proto")
			req.Header.Set("Te", "trailers")

			res, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %s", err)
			}
			defer res.Body.Close()

			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(res.Body)
				t.Fatalf("unexpected status %d: %s", res.StatusCode, body)
			}
			if res.ProtoMajor != 2 {
				t.Fatalf("front responded with %s, want HTTP/2", res.Proto)
			}
			if ct := res.Header.Get("Content-Type"); ct != "application/grpc" {
				t.Fatalf("unexpected content type %q", ct)
			}

			for i := 0; i < 3; i++ {
				msg := fmt.Sprintf("message-%d", i)
				if _, err := pw.Write([]byte(msg)); err != nil {
					t.Fatalf("failed to send %q: %s", msg, err)
				}
				got := make([]byte, len(msg))
				if _, err := io.ReadFull(res.Body, got); err != nil {
					t.Fatalf("failed to read echo of %q: %s", msg, err)
				}
				if string(got) != msg {
					t.Fatalf("got echo %q, want %q", got, msg)
				}
			}

			pw.Close()
			rest, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("failed to drain response: %s", err)
			}
			if len(rest) != 0 {
				t.Fatalf("unexpected trailing data %q", rest)
			}
			if got := res.Trailer.Get("Grpc-Status"); got != "0" {
				t.Errorf("Grpc-Status trailer = %q, want %q", got, "0")
			}
			if got := res.Trailer.Get("Grpc-Message"); got != "echo complete" {
				t.Errorf("Grpc-Message trailer = %q, want %q", got, "echo complete")
			}
		})
	}
}

// TestGRPCTrailersOnlyResponse covers the way gRPC servers report errors
// on unary calls: a single HEADERS frame carrying the status, with
// END_STREAM set and no body. The framing must survive the proxy: gRPC
// clients only treat the status headers as the final status when the
// stream ends on that HEADERS frame. Go's HTTP/2 client reports such a
// response with ContentLength 0, whereas HEADERS followed by an empty
// DATA frame yields ContentLength -1. The request is repeated because
// the failure mode is a race between flushing and handler completion.
func TestGRPCTrailersOnlyResponse(t *testing.T) {
	backend := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "gRPC requires HTTP/2, got "+r.Proto, http.StatusHTTPVersionNotSupported)
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Grpc-Status", "12")
		w.Header().Set("Grpc-Message", "unimplemented")
		// Real gRPC servers send no content-length on trailers-only
		// responses. Go's HTTP/2 server would add "content-length: 0",
		// which would let the proxy relay the status correctly even
		// when it breaks the framing. Suppress it to match the wire
		// format gRPC clients actually see.
		w.Header()["Content-Length"] = nil
		w.WriteHeader(http.StatusOK)
	}), false)
	h := newTestProxy(t, backend.Listener.Addr().String(), 80)
	front := startFront(t, h, true)
	client := h2Client(t, front, true)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	for i := 0; i < 200; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/echo.Service/Missing", http.NoBody)
		if err != nil {
			t.Fatalf("failed to create request: %s", err)
		}
		req.Host = testHost
		req.Header.Set("Content-Type", "application/grpc")
		req.Header.Set("Te", "trailers")

		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d failed: %s", i, err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatalf("request %d: failed to read body: %s", i, err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: unexpected status %d: %s", i, res.StatusCode, body)
		}
		if len(body) != 0 {
			t.Fatalf("request %d: unexpected body %q", i, body)
		}
		if got := res.Header.Get("Grpc-Status"); got != "12" {
			t.Fatalf("request %d: Grpc-Status header = %q, want %q", i, got, "12")
		}
		if got := res.Header.Get("Grpc-Message"); got != "unimplemented" {
			t.Fatalf("request %d: Grpc-Message header = %q, want %q", i, got, "unimplemented")
		}
		if res.ContentLength != 0 {
			t.Fatalf("request %d: ContentLength = %d, want 0: the stream did not end on the HEADERS frame, so a gRPC client would not see a trailers-only response", i, res.ContentLength)
		}
	}
}

// TestPlaintextBackendProtocolSelection verifies that only native gRPC is
// sent to plaintext backends as h2c. Everything else, including gRPC-Web,
// must keep using HTTP/1.1 since that is what most local servers speak.
func TestPlaintextBackendProtocolSelection(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	backend := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Proto
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}), false)
	h := newTestProxy(t, backend.Listener.Addr().String(), 80)
	front := startFront(t, h, true)
	// Browsers and gRPC clients alike reach the TLS listener over HTTP/2.
	client := h2Client(t, front, true)

	cases := []struct {
		path        string
		contentType string
		wantProto   string
	}{
		{path: "/grpc", contentType: "application/grpc", wantProto: "HTTP/2.0"},
		{path: "/grpc-proto", contentType: "application/grpc+proto", wantProto: "HTTP/2.0"},
		{path: "/grpc-web", contentType: "application/grpc-web+proto", wantProto: "HTTP/1.1"},
		{path: "/json", contentType: "application/json", wantProto: "HTTP/1.1"},
		{path: "/none", contentType: "", wantProto: "HTTP/1.1"},
	}
	for _, tc := range cases {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+tc.path, strings.NewReader("payload"))
		if err != nil {
			cancel()
			t.Fatalf("failed to create request: %s", err)
		}
		req.Host = testHost
		if tc.contentType != "" {
			req.Header.Set("Content-Type", tc.contentType)
		}
		res, err := client.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("%s: request failed: %s", tc.path, err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		cancel()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: unexpected status %d", tc.path, res.StatusCode)
		}
		if res.ProtoMajor != 2 {
			t.Fatalf("%s: front responded with %s, want HTTP/2", tc.path, res.Proto)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for _, tc := range cases {
		if got := seen[tc.path]; got != tc.wantProto {
			t.Errorf("%s (%q): backend saw %q, want %q", tc.path, tc.contentType, got, tc.wantProto)
		}
	}
}

func websocketAccept(key string) string {
	h := sha1.New()
	io.WriteString(h, key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// websocketEchoHandler performs the WebSocket handshake by hand and then
// echoes raw bytes. Framing is opaque to the proxy, so plain bytes are
// enough to prove the upgraded connection is relayed in both directions.
func websocketEchoHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
			!strings.EqualFold(r.Header.Get("Connection"), "Upgrade") {
			http.Error(w, "expected a websocket upgrade", http.StatusBadRequest)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack failed: %s", err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		if err := brw.Flush(); err != nil {
			return
		}
		io.Copy(conn, brw.Reader)
	})
}

func TestWebSocketUpgrade(t *testing.T) {
	for _, frontTLS := range []bool{false, true} {
		name := "plaintext front"
		if frontTLS {
			name = "tls front"
		}
		t.Run(name, func(t *testing.T) {
			backend := startBackend(t, websocketEchoHandler(t), false)
			h := newTestProxy(t, backend.Listener.Addr().String(), 80)
			front := startFront(t, h, frontTLS)
			conn := dialFront(t, front, frontTLS)

			// Key and expected accept value from RFC 6455, section 1.3.
			const key = "dGhlIHNhbXBsZSBub25jZQ=="
			const wantAccept = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
			fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\n"+
				"Host: %s\r\n"+
				"Upgrade: websocket\r\n"+
				"Connection: Upgrade\r\n"+
				"Sec-WebSocket-Key: %s\r\n"+
				"Sec-WebSocket-Version: 13\r\n\r\n", testHost, key)

			br := bufio.NewReader(conn)
			res, err := http.ReadResponse(br, nil)
			if err != nil {
				t.Fatalf("failed to read upgrade response: %s", err)
			}
			if res.StatusCode != http.StatusSwitchingProtocols {
				body, _ := io.ReadAll(res.Body)
				t.Fatalf("unexpected status %d: %s", res.StatusCode, body)
			}
			if got := res.Header.Get("Sec-WebSocket-Accept"); got != wantAccept {
				t.Fatalf("Sec-WebSocket-Accept = %q, want %q", got, wantAccept)
			}

			for i := 0; i < 3; i++ {
				msg := fmt.Sprintf("frame-%d", i)
				if _, err := conn.Write([]byte(msg)); err != nil {
					t.Fatalf("failed to send %q: %s", msg, err)
				}
				got := make([]byte, len(msg))
				if _, err := io.ReadFull(br, got); err != nil {
					t.Fatalf("failed to read echo of %q: %s", msg, err)
				}
				if string(got) != msg {
					t.Fatalf("got echo %q, want %q", got, msg)
				}
			}
		})
	}
}

// TestServerSentEventsAreStreamed checks that the proxy does not buffer
// streamed responses: the backend only emits the next event after the
// client acknowledged the previous one, which deadlocks (and times out)
// through a buffering proxy.
func TestServerSentEventsAreStreamed(t *testing.T) {
	const events = 3
	ack := make(chan struct{})
	backend := startBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		rc := http.NewResponseController(w)
		for i := 0; i < events; i++ {
			fmt.Fprintf(w, "data: event-%d\n\n", i)
			if err := rc.Flush(); err != nil {
				return
			}
			select {
			case <-ack:
			case <-r.Context().Done():
				return
			}
		}
	}), false)
	h := newTestProxy(t, backend.Listener.Addr().String(), 80)
	front := startFront(t, h, true)
	client := h2Client(t, front, true)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/events", nil)
	if err != nil {
		t.Fatalf("failed to create request: %s", err)
	}
	req.Host = testHost
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %s", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", res.StatusCode)
	}

	br := bufio.NewReader(res.Body)
	for i := 0; i < events; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read event %d: %s", i, err)
		}
		if want := fmt.Sprintf("data: event-%d\n", i); line != want {
			t.Fatalf("event %d = %q, want %q", i, line, want)
		}
		if _, err := br.ReadString('\n'); err != nil {
			t.Fatalf("failed to read event %d terminator: %s", i, err)
		}
		select {
		case ack <- struct{}{}:
		case <-ctx.Done():
			t.Fatalf("backend did not wait for acknowledgement of event %d", i)
		}
	}
	if rest, err := io.ReadAll(br); err != nil || len(rest) != 0 {
		t.Fatalf("unexpected trailing data %q (err: %v)", rest, err)
	}
}
