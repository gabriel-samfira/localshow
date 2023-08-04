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
	"golang.org/x/crypto/ssh"
	terminal "golang.org/x/term"
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

func NewSSHServer(ctx context.Context, cfg *config.Config) (*sshServer, error) {
	config, err := cfg.SSHServer.SSHServerConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create ssh server config: %w", err)
	}
	return &sshServer{
		config:      config,
		quit:        make(chan struct{}),
		ctx:         ctx,
		forwarders:  make(map[string]net.Listener),
		connections: make(chan net.Conn, 10),
		appConfig:   cfg,
		mux:         &sync.Mutex{},
		wg:          &sync.WaitGroup{},
	}, nil
}

type sshServer struct {
	appConfig  *config.Config
	config     *ssh.ServerConfig
	listener   net.Listener
	forwarders map[string]net.Listener
	mux        *sync.Mutex

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

func (s *sshServer) registerForwarder(fwKey string, ln net.Listener) {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.forwarders[fwKey] = ln
}

func (s *sshServer) unregisterForwarder(fwKey string) {
	s.mux.Lock()
	defer s.mux.Unlock()
	delete(s.forwarders, fwKey)
}

func (s *sshServer) forwarder(fwKey string) net.Listener {
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

func (s *sshServer) handleSSHRequest(ctx context.Context, req *ssh.Request, sshConn *ssh.ServerConn) {
	switch req.Type {
	case "tcpip-forward":
		var reqPayload remoteForwardDetails
		if err := ssh.Unmarshal(req.Payload, &reqPayload); err != nil {
			log.Print(err)
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
				return
			}

			s.registerForwarder(fwKey, ln)
			defer s.unregisterForwarder(fwKey)

			defer ln.Close()
			go func() {
				<-ctx.Done()
				ln.Close()
			}()

			destPort := ln.Addr().(*net.TCPAddr).Port
			log.Printf("listening on 127.0.11.1:%d", destPort)
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
						for {
							select {
							case <-ctx.Done():
								return
							case req := <-reqs:
								if req == nil {
									break
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
		fw := s.forwarder(reqPayload.forwarderKey(sshConn.RemoteAddr().String()))
		if fw != nil {
			fw.Close()
		}
		req.Reply(true, nil)
	default:
		log.Printf("unexpected request type: %s", req.Type)
	}
}

func (s *sshServer) handleConnection(nConn net.Conn) {
	ctx, fn := context.WithCancel(context.Background())
	defer fn()
	// Before use, a handshake must be performed on the incoming
	// net.Conn.
	conn, chans, reqs, err := ssh.NewServerConn(nConn, s.config)
	if err != nil {
		log.Printf("failed to handshake: %s", err)
		return
	}
	log.Printf("new connection from %s", conn.RemoteAddr())

	quit := make(chan struct{})
	// The incoming Request channel must be serviced.
	go func() {
		for {
			select {
			case req := <-reqs:
				if req == nil {
					break
				}
				s.handleSSHRequest(ctx, req, conn)
			case <-quit:
				log.Printf("closing connection from %s", conn.RemoteAddr())
				return
			}
		}
	}()
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

		term := terminal.NewTerminal(channel, "> ")

		go func() {
			defer channel.Close()
			defer conn.Close()
			for {
				line, err := term.ReadLine()
				if err != nil {
					break
				}
				fmt.Printf("%s\r\n", line)
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