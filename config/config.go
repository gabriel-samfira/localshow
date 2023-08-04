package config

import (
	"fmt"
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

type Config struct {
	SSHServer SSHServer `toml:"ssh_server"`
}

func (c *Config) Validate() error {
	if err := c.SSHServer.Validate(); err != nil {
		return fmt.Errorf("failed to validate ssh server config: %w", err)
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
