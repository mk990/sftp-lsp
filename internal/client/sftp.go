package client

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sshconfig "github.com/kevinburke/ssh_config"
	pkgsftp "github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"

	"sftp-lsp/internal/config"
	"sftp-lsp/internal/fs"
)

// SFTPClient connects over SSH and exposes an SFTP filesystem.
type SFTPClient struct {
	cfg        *config.Config
	sshClient  *gossh.Client
	sftp       *pkgsftp.Client
	fileSystem *sftpFileSystem
}

func NewSFTPClient(cfg *config.Config) *SFTPClient {
	return &SFTPClient{cfg: cfg}
}

func (c *SFTPClient) Connect() error {
	effective := c.applySSHConfig(c.cfg)

	sshCfg, err := c.buildSSHConfig(effective)
	if err != nil {
		return err
	}

	hops, err := effective.HopConfigs()
	if err != nil {
		return err
	}

	sshConn, err := c.dialWithHops(hops, effective, sshCfg)
	if err != nil {
		return fmt.Errorf("ssh connect to %s:%d: %w", effective.Host, effective.Port, err)
	}

	sftpClient, err := pkgsftp.NewClient(sshConn)
	if err != nil {
		sshConn.Close()
		return fmt.Errorf("open sftp subsystem: %w", err)
	}

	c.sshClient = sshConn
	c.sftp = sftpClient
	c.fileSystem = &sftpFileSystem{client: sftpClient}
	return nil
}

func (c *SFTPClient) Close() error {
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.sshClient != nil {
		return c.sshClient.Close()
	}
	return nil
}

func (c *SFTPClient) FS() fs.FileSystem { return c.fileSystem }

// applySSHConfig overlays values from ~/.ssh/config that are not already set
// in the sftp.json config.
func (c *SFTPClient) applySSHConfig(cfg *config.Config) *config.Config {
	host := cfg.Host
	sshCfgFile := expandPath("~/.ssh/config")

	f, err := os.Open(sshCfgFile)
	if err != nil {
		return cfg
	}
	defer f.Close()

	parsed, err := sshconfig.Decode(f)
	if err != nil {
		return cfg
	}

	get := func(key string) string {
		v, _ := parsed.Get(host, key)
		return v
	}

	out := *cfg // shallow copy — we only override zero-value fields

	if out.Username == "" {
		if u := get("User"); u != "" {
			out.Username = u
		}
	}
	if out.Port == 0 || out.Port == 22 {
		if p := get("Port"); p != "" {
			fmt.Sscanf(p, "%d", &out.Port)
		}
	}
	// Hostname alias (e.g. "Host paymard" → "Hostname real.host.example.com")
	if hn := get("Hostname"); hn != "" && hn != host {
		out.Host = hn
	}
	if out.PrivateKeyPath == "" {
		if id := get("IdentityFile"); id != "" {
			out.PrivateKeyPath = id
		}
	}
	// ProxyCommand stored in SSHCustomParams for use during dial.
	if out.SSHCustomParams == "" {
		if pc := get("ProxyCommand"); pc != "" {
			out.SSHCustomParams = "ProxyCommand=" + pc
		}
	}

	return &out
}

// dialWithHops connects through zero or more jump hosts, then to the target.
func (c *SFTPClient) dialWithHops(hops []config.Config, target *config.Config, targetSSHCfg *gossh.ClientConfig) (*gossh.Client, error) {
	var current *gossh.Client

	for i := range hops {
		hopSSHCfg, err := c.buildSSHConfig(&hops[i])
		if err != nil {
			return nil, fmt.Errorf("hop %d config: %w", i, err)
		}
		addr := fmt.Sprintf("%s:%d", hops[i].Host, hops[i].Port)

		var conn *gossh.Client
		if current == nil {
			conn, err = gossh.Dial("tcp", addr, hopSSHCfg)
		} else {
			netConn, err2 := current.Dial("tcp", addr)
			if err2 != nil {
				return nil, fmt.Errorf("hop %d dial: %w", i, err2)
			}
			c2, chans, reqs, err2 := gossh.NewClientConn(netConn, addr, hopSSHCfg)
			if err2 != nil {
				return nil, fmt.Errorf("hop %d handshake: %w", i, err2)
			}
			conn = gossh.NewClient(c2, chans, reqs)
		}
		if err != nil {
			return nil, fmt.Errorf("hop %d: %w", i, err)
		}
		current = conn
	}

	addr := fmt.Sprintf("%s:%d", target.Host, target.Port)
	proxyCmd := proxyCommandFrom(target.SSHCustomParams)

	if current == nil {
		// Direct dial or ProxyCommand.
		if proxyCmd != "" {
			return dialViaProxyCommand(proxyCmd, target.Host, target.Port, target.Username, targetSSHCfg)
		}
		return gossh.Dial("tcp", addr, targetSSHCfg)
	}

	// Already tunnelled through hops — open a channel through the last hop.
	netConn, err := current.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("final dial: %w", err)
	}
	c2, chans, reqs, err := gossh.NewClientConn(netConn, addr, targetSSHCfg)
	if err != nil {
		return nil, fmt.Errorf("final handshake: %w", err)
	}
	return gossh.NewClient(c2, chans, reqs), nil
}

func (c *SFTPClient) buildSSHConfig(cfg *config.Config) (*gossh.ClientConfig, error) {
	var authMethods []gossh.AuthMethod

	// 1. Explicit private key from config.
	if cfg.PrivateKeyPath != "" {
		signer, err := loadSigner(expandPath(cfg.PrivateKeyPath), cfg.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("read private key: %w", err)
		}
		authMethods = append(authMethods, gossh.PublicKeys(signer))
	}

	// 2. Default key files when no explicit key is configured.
	if cfg.PrivateKeyPath == "" && cfg.Password == "" {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			path := expandPath("~/.ssh/" + name)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			signer, err := loadSigner(path, cfg.Passphrase)
			if err != nil {
				continue
			}
			authMethods = append(authMethods, gossh.PublicKeys(signer))
			break
		}
	}

	// 3. Password auth.
	if cfg.Password != "" {
		authMethods = append(authMethods, gossh.Password(cfg.Password))
	}

	// 4. SSH agent fallback.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		agentConn, err := net.Dial("unix", sock)
		if err == nil {
			authMethods = append(authMethods, gossh.PublicKeysCallback(agent.NewClient(agentConn).Signers))
		}
	}

	// Host key verification via ~/.ssh/known_hosts.
	hostKeyCallback := gossh.InsecureIgnoreHostKey()
	if cb, err := knownhosts.New(expandPath("~/.ssh/known_hosts")); err == nil {
		hostKeyCallback = cb
	}

	return &gossh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         time.Duration(cfg.ConnectTimeout) * time.Millisecond,
	}, nil
}

// ---- ProxyCommand support ----

// proxyCommandFrom extracts the command from "ProxyCommand=<cmd>".
func proxyCommandFrom(params string) string {
	for _, part := range strings.Split(params, "\n") {
		part = strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(part, "ProxyCommand="); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// dialViaProxyCommand executes proxyCmd and uses its stdin/stdout as the SSH transport.
// Tokens %h, %p, %r in the command are replaced with host, port, and user.
func dialViaProxyCommand(proxyCmd, host string, port int, user string, sshCfg *gossh.ClientConfig) (*gossh.Client, error) {
	cmd := proxyCmd
	cmd = strings.ReplaceAll(cmd, "%h", host)
	cmd = strings.ReplaceAll(cmd, "%p", fmt.Sprintf("%d", port))
	cmd = strings.ReplaceAll(cmd, "%r", user)

	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty ProxyCommand")
	}

	proc := exec.Command(parts[0], parts[1:]...)
	proc.Stderr = os.Stderr

	stdin, err := proc.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy stdin pipe: %w", err)
	}
	stdout, err := proc.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy stdout pipe: %w", err)
	}
	if err := proc.Start(); err != nil {
		return nil, fmt.Errorf("start proxy command %q: %w", parts[0], err)
	}

	pconn := &proxyConn{
		reader: stdout,
		writer: stdin,
		cmd:    proc,
		done:   make(chan struct{}),
		remote: &net.TCPAddr{IP: net.ParseIP(host), Port: port},
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	c, chans, reqs, err := gossh.NewClientConn(pconn, addr, sshCfg)
	if err != nil {
		pconn.Close()
		return nil, fmt.Errorf("ssh handshake via proxy: %w", err)
	}
	return gossh.NewClient(c, chans, reqs), nil
}

// proxyConn wraps a proxy command's stdin/stdout as a net.Conn.
// remote must be the real host:port so that knownhosts can verify the key.
type proxyConn struct {
	reader io.ReadCloser
	writer io.WriteCloser
	cmd    *exec.Cmd
	once   sync.Once
	done   chan struct{}
	remote net.Addr
}

func (p *proxyConn) Read(b []byte) (int, error)  { return p.reader.Read(b) }
func (p *proxyConn) Write(b []byte) (int, error) { return p.writer.Write(b) }

func (p *proxyConn) Close() error {
	p.once.Do(func() {
		p.writer.Close()
		p.reader.Close()
		p.cmd.Wait()
		close(p.done)
	})
	return nil
}

func (p *proxyConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (p *proxyConn) RemoteAddr() net.Addr               { return p.remote }
func (p *proxyConn) SetDeadline(_ time.Time) error      { return nil }
func (p *proxyConn) SetReadDeadline(_ time.Time) error  { return nil }
func (p *proxyConn) SetWriteDeadline(_ time.Time) error { return nil }

// ---- helpers ----

// expandPath replaces a leading ~ with the user's home directory.
func expandPath(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

func loadSigner(path, passphrase string) (gossh.Signer, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if passphrase != "" {
		return gossh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
	}
	return gossh.ParsePrivateKey(key)
}

// ---- sftpFileSystem ----

type sftpFileSystem struct {
	client *pkgsftp.Client
}

func (s *sftpFileSystem) Stat(path string) (*fs.FileInfo, error) {
	fi, err := s.client.Stat(path)
	if err != nil {
		return nil, err
	}
	return fromOSInfo(fi), nil
}

func (s *sftpFileSystem) List(path string) ([]*fs.FileInfo, error) {
	entries, err := s.client.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]*fs.FileInfo, 0, len(entries))
	for _, e := range entries {
		result = append(result, fromOSInfo(e))
	}
	return result, nil
}

func (s *sftpFileSystem) Get(path string) (io.ReadCloser, error) {
	return s.client.Open(path)
}

func (s *sftpFileSystem) Put(path string, content io.Reader, perm os.FileMode) error {
	f, err := s.client.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, content); err != nil {
		return err
	}
	if perm != 0 {
		return s.client.Chmod(path, perm)
	}
	return nil
}

func (s *sftpFileSystem) MkdirAll(path string, perm os.FileMode) error {
	if err := s.client.MkdirAll(path); err != nil {
		return err
	}
	if perm != 0 {
		return s.client.Chmod(path, perm)
	}
	return nil
}

func (s *sftpFileSystem) Remove(path string) error      { return s.client.Remove(path) }
func (s *sftpFileSystem) RemoveAll(path string) error   { return s.client.RemoveAll(path) }
func (s *sftpFileSystem) Rename(old, new string) error  { return s.client.Rename(old, new) }
func (s *sftpFileSystem) Chmod(path string, mode os.FileMode) error {
	return s.client.Chmod(path, mode)
}

func fromOSInfo(fi os.FileInfo) *fs.FileInfo {
	return &fs.FileInfo{
		Name:    fi.Name(),
		Size:    fi.Size(),
		Mode:    fi.Mode(),
		ModTime: fi.ModTime(),
		IsDir:   fi.IsDir(),
	}
}
