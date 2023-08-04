package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"

	//"encoding/json"
	"os"

	"golang.org/x/crypto/ssh"
	terminal "golang.org/x/term"
)

const (
	forwardedTCPChannelType = "forwarded-tcpip"
)

type remoteForwardRequest struct {
	BindAddr string
	BindPort uint32
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

func main() {
	if err := GenerateKey("/tmp/testcert"); err != nil {
		log.Fatal(err)
	}
	// Public key authentication is done by comparing
	// the public key of a received connection
	// with the entries in the authorized_keys file.
	// authorizedKeysBytes, err := os.ReadFile("/home/gabriel/.ssh/authorized_keys")
	// if err != nil {
	// 	log.Fatalf("Failed to load authorized_keys, err: %v", err)
	// }

	// authorizedKeysMap := map[string]bool{}
	// for len(authorizedKeysBytes) > 0 {
	// 	pubKey, _, _, rest, err := ssh.ParseAuthorizedKey(authorizedKeysBytes)
	// 	if err != nil {
	// 		log.Fatal(err)
	// 	}

	// 	authorizedKeysMap[string(pubKey.Marshal())] = true
	// 	authorizedKeysBytes = rest
	// }

	// An SSH server is represented by a ServerConfig, which holds
	// certificate details and handles authentication of ServerConns.
	config := &ssh.ServerConfig{
		// Remove to disable password auth.
		NoClientAuth:     true,
		PasswordCallback: nil,
		// PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		// 	// allow everyone. This is a demo.
		// 	return &ssh.Permissions{
		// 		// Record the public key used for authentication.
		// 		Extensions: map[string]string{
		// 			"password": "",
		// 		},
		// 	}, nil
		// },
		PublicKeyCallback: nil,
		// // Remove to disable public key auth.
		// PublicKeyCallback: func(c ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
		// 	if authorizedKeysMap[string(pubKey.Marshal())] {
		// 		return &ssh.Permissions{
		// 			// Record the public key used for authentication.
		// 			Extensions: map[string]string{
		// 				"pubkey-fp": ssh.FingerprintSHA256(pubKey),
		// 			},
		// 		}, nil
		// 	}
		// 	return nil, fmt.Errorf("unknown public key for %q", c.User())
		// },
	}

	privateBytes, err := os.ReadFile("/tmp/testcert")
	if err != nil {
		log.Fatal("Failed to load private key: ", err)
	}

	private, err := ssh.ParsePrivateKey(privateBytes)
	if err != nil {
		log.Fatal("Failed to parse private key: ", err)
	}

	config.AddHostKey(private)

	// Once a ServerConfig has been configured, connections can be
	// accepted.
	listener, err := net.Listen("tcp", "0.0.0.0:2022")
	if err != nil {
		log.Fatal("failed to listen for connection: ", err)
	}
	for {
		nConn, err := listener.Accept()
		if err != nil {
			log.Printf("failed to accept incoming connection: %s", err)
			return
		}

		// Before use, a handshake must be performed on the incoming
		// net.Conn.
		conn, chans, reqs, err := ssh.NewServerConn(nConn, config)
		if err != nil {
			log.Printf("failed to handshake: %s", err)
			continue
		}
		log.Printf("logged in with permissions %v", conn.Permissions)

		quit := make(chan struct{})
		// The incoming Request channel must be serviced.
		go func() {
			for {
				select {
				case req := <-reqs:
					if req == nil {
						break
					}
					switch req.Type {
					case "tcpip-forward":
						var reqPayload remoteForwardRequest
						if err := ssh.Unmarshal(req.Payload, &reqPayload); err != nil {
							log.Print(err)
							continue
						}

						go func(reqPayload remoteForwardRequest) {
							// Allocate a random port. We'll lie to the client that we actually
							// bound to the requested port.
							ln, err := net.Listen("tcp", "127.0.11.1:0")
							if err != nil {
								log.Printf("failed to listen: %s", err)
								return
							}

							go func() {
								<-quit
								ln.Close()
							}()
							destPort := ln.Addr().(*net.TCPAddr).Port
							log.Printf("actual port is: %d", destPort)
							log.Printf("listening on %s:%d", reqPayload.BindAddr, reqPayload.BindPort)
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
									ch, reqs, err := conn.OpenChannel(forwardedTCPChannelType, payload)
									if err != nil {
										log.Println(err)
										c.Close()
										return
									}
									go func() {
										for {
											select {
											case <-quit:
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
					default:
						log.Printf("unexpected request type: %s", req.Type)
					}
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
}
