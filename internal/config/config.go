package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Protocol string

const (
	ProtocolSFTP  Protocol = "sftp"
	ProtocolFTP   Protocol = "ftp"
	ProtocolLocal Protocol = "local"
)

type SyncOption struct {
	Delete         bool `json:"delete"`
	SkipCreate     bool `json:"skipCreate"`
	IgnoreExisting bool `json:"ignoreExisting"`
	Update         bool `json:"update"`
	BothDirections bool `json:"bothDirections"`
}

type WatcherConfig struct {
	Files      string `json:"files"`
	AutoUpload bool   `json:"autoUpload"`
	AutoDelete bool   `json:"autoDelete"`
}

type RemoteExplorerConfig struct {
	FilesExclude []string `json:"filesExclude"`
	Order        int      `json:"order"`
}

// Config mirrors the sftp.json schema.
type Config struct {
	Name    string   `json:"name"`
	Context string   `json:"context"`
	Profile string   `json:"profile"`

	// Connection
	Protocol       Protocol `json:"protocol"`
	Host           string   `json:"host"`
	Port           int      `json:"port"`
	Username       string   `json:"username"`
	Password       string   `json:"password"`
	ConnectTimeout int      `json:"connectTimeout"` // milliseconds

	// SSH-specific
	Agent           string      `json:"agent"`
	PrivateKeyPath  string      `json:"privateKeyPath"`
	Passphrase      string      `json:"passphrase"`
	InteractiveAuth interface{} `json:"interactiveAuth"` // bool or []string
	Algorithms      interface{} `json:"algorithms"`
	SSHConfigPath   string      `json:"sshConfigPath"`
	SSHCustomParams string      `json:"sshCustomParams"`
	// Maximum open file descriptors on remote (SFTP); 0 = unlimited.
	LimitOpenFilesOnRemote interface{} `json:"limitOpenFilesOnRemote"`

	// SSH hopping: single hop config or array.
	Hop json.RawMessage `json:"hop"`

	// FTP-specific
	Secure        interface{} `json:"secure"`        // bool, "control", "implicit"
	SecureOptions interface{} `json:"secureOptions"` // tls.Config-like object
	Passive       bool        `json:"passive"`

	// Paths & behaviour
	RemotePath     string      `json:"remotePath"`
	UploadOnSave   bool        `json:"uploadOnSave"`
	DownloadOnOpen interface{} `json:"downloadOnOpen"` // bool or "confirm"
	UseTempFile    bool        `json:"useTempFile"`
	OpenSsh        bool        `json:"openSsh"`

	// Transfer tuning
	Concurrency           int     `json:"concurrency"`
	FilePerm              int     `json:"filePerm"` // octal, e.g. 0644
	DirPerm               int     `json:"dirPerm"`  // octal, e.g. 0755
	RemoteTimeOffsetHours float64 `json:"remoteTimeOffsetInHours"`

	// File filtering
	Ignore     []string      `json:"ignore"`
	IgnoreFile string        `json:"ignoreFile"`
	Watcher    WatcherConfig `json:"watcher"`

	// Sync behaviour
	SyncOption SyncOption `json:"syncOption"`

	// Remote explorer
	RemoteExplorer RemoteExplorerConfig `json:"remoteExplorer"`
}

func (c *Config) SetDefaults() {
	if c.Protocol == "" {
		c.Protocol = ProtocolSFTP
	}
	if c.Port == 0 {
		switch c.Protocol {
		case ProtocolSFTP:
			c.Port = 22
		case ProtocolFTP:
			c.Port = 21
		}
	}
	if c.Concurrency == 0 {
		c.Concurrency = 4
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 10_000
	}
}

func (c *Config) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("host is required")
	}
	if c.Username == "" {
		return fmt.Errorf("username is required")
	}
	if c.RemotePath == "" {
		return fmt.Errorf("remotePath is required")
	}
	return nil
}

// HopConfigs returns the list of intermediate hop configs, if any.
func (c *Config) HopConfigs() ([]Config, error) {
	if c.Hop == nil {
		return nil, nil
	}
	// Try array first.
	var hops []Config
	if err := json.Unmarshal(c.Hop, &hops); err == nil {
		for i := range hops {
			hops[i].SetDefaults()
		}
		return hops, nil
	}
	// Fall back to single object.
	var hop Config
	if err := json.Unmarshal(c.Hop, &hop); err != nil {
		return nil, fmt.Errorf("parse hop: %w", err)
	}
	hop.SetDefaults()
	return []Config{hop}, nil
}

// Load reads sftp.json (single object or array) and returns all profiles.
func Load(path string) ([]Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var configs []Config
	if err := json.Unmarshal(data, &configs); err != nil {
		// Not an array — try single object.
		var single Config
		if err2 := json.Unmarshal(data, &single); err2 != nil {
			return nil, fmt.Errorf("parse config: %w", err2)
		}
		configs = []Config{single}
	}

	for i := range configs {
		configs[i].SetDefaults()
		if err := configs[i].Validate(); err != nil {
			name := configs[i].Name
			if name == "" {
				name = fmt.Sprintf("[%d]", i)
			}
			return nil, fmt.Errorf("config %s: %w", name, err)
		}
	}
	return configs, nil
}

// FindConfigFile walks up from startDir looking for .vscode/sftp.json.
func FindConfigFile(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, ".vscode", "sftp.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("sftp.json not found in %s or any parent directory", startDir)
}
