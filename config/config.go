package config

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
)

func NewConfig(cfgFile string) (*Config, error) {
	var config Config
	if _, err := toml.DecodeFile(cfgFile, &config); err != nil {
		return nil, errors.Wrap(err, "decoding toml")
	}
	if err := config.Validate(); err != nil {
		return nil, errors.Wrap(err, "validating config")
	}
	return &config, nil
}

type SSHServer struct {
	BindAddress string `toml:"bind_address"`
	BindPort    int    `toml:"bind_port"`

	HostKeyPath        string `toml:"host_key_path"`
	AuthorizedKeysPath string `toml:"authorized_keys_path"`
	DisableAuth        bool   `toml:"disable_auth"`
}

func (c SSHServer) Validate() error {
	if c.HostKeyPath == "" {
		return fmt.Errorf("host key path is required")
	}

	if !c.DisableAuth && c.AuthorizedKeysPath == "" {
		return fmt.Errorf("authorized keys path is required when auth is enabled")
	}
	return nil
}

type HTTPServer struct {
	BindAddr string `toml:"bind_address"`
	BindPort int    `toml:"bind_port"`

	ExcludedSubdomains []string `toml:"excluded_subdomains"`
	DomainName         string   `toml:"domain_name"`

	UseTLS    bool      `toml:"use_tls" json:"use-tls"`
	TLSConfig TLSConfig `toml:"tls" json:"tls"`
}

func (a *HTTPServer) Validate() error {
	if a.UseTLS {
		if err := a.TLSConfig.Validate(); err != nil {
			return fmt.Errorf("failed to validate tls config: %w", err)
		}
	}
	if a.BindPort > 65535 || a.BindPort < 1 {
		return fmt.Errorf("invalid port nr %d", a.BindPort)
	}

	ip := net.ParseIP(a.BindAddr)
	if ip == nil {
		// No need for deeper validation here, as any invalid
		// IP address specified in this setting will raise an error
		// when we try to bind to it.
		return fmt.Errorf("invalid IP address")
	}
	return nil
}

// BindAddress returns a host:port string.
func (a *HTTPServer) BindAddress() string {
	return fmt.Sprintf("%s:%d", a.BindAddr, a.BindPort)
}

type TLSConfig struct {
	CRT string `toml:"certificate" json:"certificate"`
	Key string `toml:"key" json:"key"`
}

// Validate validates the TLS config
func (t *TLSConfig) Validate() error {
	if t.CRT == "" || t.Key == "" {
		return fmt.Errorf("missing crt or key")
	}

	_, err := tls.LoadX509KeyPair(t.CRT, t.Key)
	if err != nil {
		return err
	}
	return nil
}

type Config struct {
	SSHServer  SSHServer  `toml:"ssh_server"`
	HTTPServer HTTPServer `toml:"http_server"`
}

func (c *Config) Validate() error {
	if err := c.SSHServer.Validate(); err != nil {
		return fmt.Errorf("failed to validate ssh server config: %w", err)
	}

	if err := c.HTTPServer.Validate(); err != nil {
		return fmt.Errorf("failed to validate http server config: %w", err)
	}
	return nil
}

func (c SSHServer) authorizedKeysMap() map[string]bool {
	authorizedKeysMap := map[string]bool{}
	if c.AuthorizedKeysPath == "" {
		return authorizedKeysMap
	}

	authorizedKeysBytes, err := os.ReadFile(c.AuthorizedKeysPath)
	if err != nil {
		return authorizedKeysMap
	}

	for len(authorizedKeysBytes) > 0 {
		pubKey, _, _, rest, err := ssh.ParseAuthorizedKey(authorizedKeysBytes)
		if err != nil {
			return authorizedKeysMap
		}

		authorizedKeysMap[string(pubKey.Marshal())] = true
		authorizedKeysBytes = rest
	}
	return authorizedKeysMap
}

func (c SSHServer) SSHServerConfig() (*ssh.ServerConfig, error) {
	cfg := &ssh.ServerConfig{
		// Remove to disable password auth.
		NoClientAuth: c.DisableAuth,
		// Remove to disable public key auth.
		PublicKeyCallback: nil,
	}

	if !c.DisableAuth {
		authorizedKeysMap := c.authorizedKeysMap()
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
			if authorizedKeysMap[string(pubKey.Marshal())] {
				return &ssh.Permissions{
					// Record the public key used for authentication.
					Extensions: map[string]string{
						"pubkey-fp": ssh.FingerprintSHA256(pubKey),
					},
				}, nil
			}
			return nil, fmt.Errorf("unknown public key for %q", c.User())
		}
	}

	privateBytes, err := os.ReadFile(c.HostKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load private key: %w", err)
	}

	private, err := ssh.ParsePrivateKey(privateBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	cfg.AddHostKey(private)
	return cfg, nil
}
