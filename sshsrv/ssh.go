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

package sshsrv

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gabriel-samfira/localshow/config"
	"github.com/gabriel-samfira/localshow/params"
	"golang.org/x/crypto/ssh"
	terminal "golang.org/x/term"

	"github.com/castillobgr/sententia"
)

const (
	forwardedTCPChannelType = "forwarded-tcpip"
)

type remoteForwardDetails struct {
	BindAddr string
	BindPort uint32
}

func (r *remoteForwardDetails) forwarderKey(tag string) string {
	return fmt.Sprintf("%s:%s:%d", tag, r.BindAddr, r.BindPort)
}

type remoteForwardSuccess struct {
	BindPort uint32
}

type remoteForwardChannelData struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

func GenerateKey(pth string) error {
	if _, err := os.Stat(pth); err == nil {
		return nil
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}

	keyOut, err := os.OpenFile(pth, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to open %s for writing: %w", pth, err)
	}

	pemBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}
	if err := pem.Encode(keyOut, pemBlock); err != nil {
		return fmt.Errorf("failed to write data to %s: %s", pth, err)
	}

	return nil
}

func NewSSHServer(ctx context.Context, cfg *config.Config, tunnelEvents chan params.TunnelEvent) (*sshServer, error) {
	config, err := cfg.SSHServer.SSHServerConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create ssh server config: %w", err)
	}
	return &sshServer{
		config:       config,
		quit:         make(chan struct{}),
		ctx:          ctx,
		forwarders:   make(map[string]*forwarderDetails),
		subdomains:   make(map[string]struct{}),
		connections:  make(chan net.Conn, 10),
		appConfig:    cfg,
		mux:          &sync.Mutex{},
		wg:           &sync.WaitGroup{},
		tunnelEvents: tunnelEvents,
	}, nil
}

type forwarderDetails struct {
	listener  net.Listener
	subdomain string
	bindAddr  string
	bindPort  uint32

	msgChan chan params.NotifyMessage
	errChan chan error
}

type sshServer struct {
	appConfig    *config.Config
	config       *ssh.ServerConfig
	listener     net.Listener
	subdomains   map[string]struct{}
	forwarders   map[string]*forwarderDetails
	mux          *sync.Mutex
	tunnelEvents chan params.TunnelEvent

	connections chan net.Conn

	ctx  context.Context
	wg   *sync.WaitGroup
	quit chan struct{}
}

func (s *sshServer) loop() {
	s.wg.Add(1)
	defer func() {
		s.wg.Done()
		s.listener.Close()
	}()

	listenerClosed := make(chan struct{})
	go func() {
		for {
			nConn, err := s.listener.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					close(listenerClosed)
					return
				}
				log.Printf("failed to accept incoming connection: %s", err)
				continue
			}
			s.connections <- nConn
		}
	}()

	for {
		select {
		case <-s.ctx.Done():
			return
		case conn := <-s.connections:
			go s.handleConnection(conn)
		case <-s.quit:
			log.Printf("ssh server quit")
			return
		case <-listenerClosed:
			log.Printf("ssh server listener closed")
			return
		}
	}
}

func (s *sshServer) registerForwarder(fwKey string, details forwarderDetails) error {
	s.mux.Lock()
	defer s.mux.Unlock()
	if details.subdomain == "" || details.subdomain == "localhost" {
		subdomain, _ := sententia.Make("{{ adjective }}-{{ noun }}")
		details.subdomain = subdomain
	}

	if _, ok := s.forwarders[fwKey]; ok {
		return fmt.Errorf("forwarder already registered")
	}

	if _, ok := s.subdomains[details.subdomain]; ok {
		return fmt.Errorf("subdomain already registered")
	}

	log.Printf("registering tunnel with key %s", fwKey)
	s.subdomains[details.subdomain] = struct{}{}
	s.forwarders[fwKey] = &details

	s.tunnelEvents <- params.TunnelEvent{
		EventType:          params.EventTypeTunnelReady,
		NotifyChan:         details.msgChan,
		ErrorChan:          details.errChan,
		BindAddr:           details.bindAddr,
		RequestedPort:      details.bindPort,
		RequestedSubdomain: details.subdomain,
	}

	return nil
}

func (s *sshServer) unregisterForwarder(fwKey string) {
	s.mux.Lock()
	defer s.mux.Unlock()
	fw, ok := s.forwarders[fwKey]
	if !ok {
		return
	}

	log.Printf("unregistering tunnel with key %s", fwKey)
	fw.listener.Close()
	delete(s.forwarders, fwKey)
	delete(s.subdomains, fw.subdomain)
	s.tunnelEvents <- params.TunnelEvent{
		EventType:          params.EventTypeTunnelClosed,
		NotifyChan:         nil,
		ErrorChan:          nil,
		BindAddr:           fw.bindAddr,
		RequestedSubdomain: fw.subdomain,
	}
}

func (s *sshServer) forwarder(fwKey string) *forwarderDetails {
	s.mux.Lock()
	defer s.mux.Unlock()
	return s.forwarders[fwKey]
}

func (s *sshServer) hasForwarder(fwKey string) bool {
	s.mux.Lock()
	defer s.mux.Unlock()
	_, ok := s.forwarders[fwKey]
	return ok
}

func (s *sshServer) handleSSHRequest(ctx context.Context, req *ssh.Request, sshConn *ssh.ServerConn, msgChan chan params.NotifyMessage, errChan chan error) {
	switch req.Type {
	case "tcpip-forward":
		var reqPayload remoteForwardDetails
		if err := ssh.Unmarshal(req.Payload, &reqPayload); err != nil {
			log.Print(err)
			req.Reply(false, nil)
			return
		}

		if reqPayload.BindPort != 80 && reqPayload.BindPort != 443 {
			// We only support forwarding http and https.
			errChan <- fmt.Errorf("unsupported port: %d", reqPayload.BindPort)
			req.Reply(false, nil)
			return
		}

		fwKey := reqPayload.forwarderKey(sshConn.RemoteAddr().String())
		if s.hasForwarder(fwKey) {
			// We're already forwarding this hos:port pair from the same client.
			req.Reply(false, nil)
			return
		}

		go func(reqPayload remoteForwardDetails) {
			// Allocate a random port. We'll lie to the client that we actually
			// bound to the requested port.
			ln, err := net.Listen("tcp", "127.0.11.1:0")
			if err != nil {
				log.Printf("failed to listen: %s", err)
				req.Reply(false, nil)
				return
			}
			defer ln.Close()
			go func() {
				<-ctx.Done()
				ln.Close()
			}()

			destPort := ln.Addr().(*net.TCPAddr).Port
			err = s.registerForwarder(fwKey, forwarderDetails{
				listener:  ln,
				subdomain: reqPayload.BindAddr,
				bindAddr:  fmt.Sprintf("127.0.11.1:%d", destPort),
				bindPort:  reqPayload.BindPort,
				msgChan:   msgChan,
				errChan:   errChan,
			})
			if err != nil {
				log.Printf("failed to register forwarder: %s", err)
				errChan <- fmt.Errorf("failed to register forwarder: %s", err)
				req.Reply(false, nil)
				return
			}
			defer s.unregisterForwarder(fwKey)

			msg := fmt.Sprintf("Listening on local address 127.0.11.1:%d", destPort)
			log.Println(msg)
			for {
				c, err := ln.Accept()
				if err != nil {
					if !errors.Is(err, net.ErrClosed) {
						log.Printf("failed to accept: %s", err)
					}
					return
				}

				log.Printf("accepted connection from %s", c.RemoteAddr())
				originAddr, orignPortStr, _ := net.SplitHostPort(c.RemoteAddr().String())
				originPort, _ := strconv.Atoi(orignPortStr)
				payload := ssh.Marshal(&remoteForwardChannelData{
					DestAddr: reqPayload.BindAddr,
					// Not the actual port we're listening on.
					DestPort:   uint32(reqPayload.BindPort),
					OriginAddr: originAddr,
					OriginPort: uint32(originPort),
				})

				go func() {
					log.Printf("opening channel for %s:%d", originAddr, originPort)
					ch, reqs, err := sshConn.OpenChannel(forwardedTCPChannelType, payload)
					if err != nil {
						log.Println(err)
						c.Close()
						return
					}
					log.Printf("opened channel for %s:%d", reqPayload.BindAddr, reqPayload.BindPort)
					go func() {
						defer ch.Close()
						defer c.Close()
						for {
							select {
							case <-ctx.Done():
								return
							case req := <-reqs:
								if req == nil {
									return
								}
								log.Printf("Got request of type: %s", req.Type)
							}
						}
					}()
					go func() {
						defer ch.Close()
						defer c.Close()
						io.Copy(ch, c)
					}()
					go func() {
						defer ch.Close()
						defer c.Close()
						io.Copy(c, ch)
					}()
				}()
			}
		}(reqPayload)
		// TODO: Check if we actually bound to the requested port.
		req.Reply(true, ssh.Marshal(&remoteForwardSuccess{uint32(reqPayload.BindPort)}))
	case "cancel-tcpip-forward":
		var reqPayload remoteForwardDetails
		if err := ssh.Unmarshal(req.Payload, &reqPayload); err != nil {
			log.Printf("failed to unmarshal payload: %s", err)
			return
		}
		fwKey := reqPayload.forwarderKey(sshConn.RemoteAddr().String())
		fw := s.forwarder(fwKey)
		if fw != nil {
			fw.listener.Close()
			s.unregisterForwarder(fwKey)
		}
		req.Reply(true, nil)
	case "keepalive@openssh.com":
		req.Reply(true, nil)
	default:
		log.Printf("unexpected request type: %s", req.Type)
		req.Reply(false, nil)
	}
}

func (s *sshServer) handleConnection(nConn net.Conn) {
	ctx, fn := context.WithCancel(context.Background())
	defer fn()
	// Before use, a handshake must be performed on the incoming
	// net.Conn.
	conn, chans, reqs, err := ssh.NewServerConn(nConn, s.config)
	if err != nil {
		log.Printf("failed to handshake %s: %s", nConn.RemoteAddr(), err)
		return
	}
	log.Printf("handshake successful for connection from %s", conn.RemoteAddr())
	user := conn.Permissions.Extensions["username"]

	log.Printf("new connection from %s", conn.RemoteAddr())

	quit := make(chan struct{})
	msgChan := make(chan params.NotifyMessage, 10)
	errChan := make(chan error, 1)
	// The incoming Request channel must be serviced.
	go func() {
		for {
			select {
			case req := <-reqs:
				if req == nil {
					return
				}
				s.handleSSHRequest(ctx, req, conn, msgChan, errChan)
			case <-quit:
				log.Printf("closing connection from %s", conn.RemoteAddr())
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	logFmt := stringFormat
	if user == "api" {
		logFmt = jsonFormat
	}
	msgHandler := newMessageHandler(ctx, msgChan, errChan, logFmt, s.appConfig.HTTPServer.UseTLS)
	defer msgHandler.Close()

	// Service the incoming Channel channel.
	for newChannel := range chans {
		// Channels have a type, depending on the application level
		// protocol intended. In the case of a shell, the type is
		// "session" and ServerShell may be used to present a simple
		// terminal interface.
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Fatalf("Could not accept channel: %v", err)
		}

		// Sessions have out-of-band requests such as "shell",
		// "pty-req" and "env".  Here we handle only the
		// "shell" request.
		go func(in <-chan *ssh.Request) {
			for req := range in {
				switch req.Type {
				case "shell":
					req.Reply(req.Type == "shell", nil)
				default:
					log.Printf("unexpected request type: %s", req.Type)
				}
			}
		}(requests)

		var prompt string
		if user != "api" {
			prompt = "> "
		}

		go func() {
			defer channel.Close()
			defer conn.Close()
			var messageID string
			term := terminal.NewTerminal(channel, prompt)
			messageID = msgHandler.Register(term)
			defer msgHandler.Unregister(messageID)
			msgHandler.Urls(messageID)

			go func() {
				defer channel.Close()
				defer conn.Close()
				defer msgHandler.Close()
				err := msgHandler.Wait()
				if err != nil {
					log.Print(err)
				}
			}()

			for {
				line, err := term.ReadLine()
				if err != nil {
					break
				}
				switch line {
				case "logs":
					msgHandler.SetLogging(messageID, true)
					term.Write([]byte("Logging enabled\n"))
				case "nologs":
					msgHandler.SetLogging(messageID, false)
					term.Write([]byte("Logging disabled\n"))
				case "quit":
					return
				}
			}
		}()
	}
	nConn.Close()
	close(quit)
	log.Printf("closed connection from %s", conn.RemoteAddr())
}

func (s *sshServer) Start() error {
	listener, err := net.Listen("tcp", net.JoinHostPort(s.appConfig.SSHServer.BindAddress, fmt.Sprintf("%d", s.appConfig.SSHServer.BindPort)))
	if err != nil {
		return fmt.Errorf("failed to listen for connection: %w", err)
	}

	s.listener = listener
	go s.loop()
	return nil
}

func (s *sshServer) Wait() error {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		return fmt.Errorf("timed out waiting for ssh server to stop")
	}
	return nil
}

func (s *sshServer) Stop() error {
	close(s.quit)
	return nil
}
