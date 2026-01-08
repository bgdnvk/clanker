package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient provides SSH connection and command execution
type SSHClient struct {
	host       string
	port       int
	user       string
	privateKey []byte
	client     *ssh.Client
	debug      bool
}

// SSHClientOptions contains options for creating an SSH client
type SSHClientOptions struct {
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	PrivateKey     []byte
	Debug          bool
}

// NewSSHClient creates a new SSH client
func NewSSHClient(opts SSHClientOptions) (*SSHClient, error) {
	var privateKey []byte
	var err error

	if len(opts.PrivateKey) > 0 {
		privateKey = opts.PrivateKey
	} else if opts.PrivateKeyPath != "" {
		privateKey, err = os.ReadFile(opts.PrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read private key: %w", err)
		}
	} else {
		// Try default SSH key locations
		home, _ := os.UserHomeDir()
		keyPaths := []string{
			filepath.Join(home, ".ssh", "id_rsa"),
			filepath.Join(home, ".ssh", "id_ed25519"),
		}
		for _, path := range keyPaths {
			if data, err := os.ReadFile(path); err == nil {
				privateKey = data
				break
			}
		}
		if len(privateKey) == 0 {
			return nil, fmt.Errorf("no SSH private key found")
		}
	}

	port := opts.Port
	if port == 0 {
		port = 22
	}

	user := opts.User
	if user == "" {
		user = "ubuntu"
	}

	return &SSHClient{
		host:       opts.Host,
		port:       port,
		user:       user,
		privateKey: privateKey,
		debug:      opts.Debug,
	}, nil
}

// Connect establishes an SSH connection
func (c *SSHClient) Connect(ctx context.Context) error {
	signer, err := ssh.ParsePrivateKey(c.privateKey)
	if err != nil {
		return fmt.Errorf("failed to parse private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: c.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", c.host, c.port)

	if c.debug {
		fmt.Printf("[ssh] connecting to %s@%s\n", c.user, addr)
	}

	// Use context for connection timeout
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return fmt.Errorf("failed to create SSH connection: %w", err)
	}

	c.client = ssh.NewClient(sshConn, chans, reqs)

	if c.debug {
		fmt.Printf("[ssh] connected to %s\n", addr)
	}

	return nil
}

// Close closes the SSH connection
func (c *SSHClient) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

// Run executes a command on the remote host
func (c *SSHClient) Run(ctx context.Context, command string) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("not connected")
	}

	session, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	if c.debug {
		fmt.Printf("[ssh] running: %s\n", command)
	}

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Create a channel to signal command completion
	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	// Wait for command or context cancellation
	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("command failed: %w, stderr: %s", err, stderr.String())
		}
		return stdout.String(), nil
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return "", ctx.Err()
	}
}

// RunSudo executes a command with sudo
func (c *SSHClient) RunSudo(ctx context.Context, command string) (string, error) {
	return c.Run(ctx, "sudo "+command)
}

// RunScript executes a multi-line script
func (c *SSHClient) RunScript(ctx context.Context, script string) (string, error) {
	// Escape single quotes in the script
	escaped := strings.ReplaceAll(script, "'", "'\"'\"'")
	return c.Run(ctx, fmt.Sprintf("bash -c '%s'", escaped))
}

// RunSudoScript executes a multi-line script with sudo
func (c *SSHClient) RunSudoScript(ctx context.Context, script string) (string, error) {
	escaped := strings.ReplaceAll(script, "'", "'\"'\"'")
	return c.Run(ctx, fmt.Sprintf("sudo bash -c '%s'", escaped))
}

// Upload copies a file to the remote host
func (c *SSHClient) Upload(ctx context.Context, localPath, remotePath string) error {
	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	// Read local file
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("failed to read local file: %w", err)
	}

	return c.UploadBytes(ctx, data, remotePath)
}

// UploadBytes copies bytes to a remote file
func (c *SSHClient) UploadBytes(ctx context.Context, data []byte, remotePath string) error {
	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	if c.debug {
		fmt.Printf("[ssh] uploading %d bytes to %s\n", len(data), remotePath)
	}

	// Use stdin to pipe the file content
	go func() {
		w, _ := session.StdinPipe()
		defer w.Close()
		_, _ = fmt.Fprintf(w, "C0644 %d %s\n", len(data), filepath.Base(remotePath))
		_, _ = w.Write(data)
		_, _ = fmt.Fprint(w, "\x00")
	}()

	dir := filepath.Dir(remotePath)
	return session.Run(fmt.Sprintf("scp -t %s", dir))
}

// Download copies a file from the remote host
func (c *SSHClient) Download(ctx context.Context, remotePath, localPath string) error {
	if c.client == nil {
		return fmt.Errorf("not connected")
	}

	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	if c.debug {
		fmt.Printf("[ssh] downloading %s to %s\n", remotePath, localPath)
	}

	var stdout bytes.Buffer
	session.Stdout = &stdout

	if err := session.Run(fmt.Sprintf("cat %s", remotePath)); err != nil {
		return fmt.Errorf("failed to read remote file: %w", err)
	}

	return os.WriteFile(localPath, stdout.Bytes(), 0644)
}

// DownloadBytes reads a remote file and returns its contents
func (c *SSHClient) DownloadBytes(ctx context.Context, remotePath string) ([]byte, error) {
	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	session, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	if c.debug {
		fmt.Printf("[ssh] reading %s\n", remotePath)
	}

	var stdout bytes.Buffer
	session.Stdout = &stdout

	if err := session.Run(fmt.Sprintf("cat %s", remotePath)); err != nil {
		return nil, fmt.Errorf("failed to read remote file: %w", err)
	}

	return stdout.Bytes(), nil
}

// WaitForSSH waits for SSH to become available
func WaitForSSH(ctx context.Context, host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("%s:%d", host, port)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}

		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("timeout waiting for SSH on %s", addr)
}

// SSHSession represents an interactive SSH session
type SSHSession struct {
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  io.Reader
}

// NewInteractiveSession creates an interactive SSH session
func (c *SSHClient) NewInteractiveSession() (*SSHSession, error) {
	if c.client == nil {
		return nil, fmt.Errorf("not connected")
	}

	session, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := session.Shell(); err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to start shell: %w", err)
	}

	return &SSHSession{
		session: session,
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
	}, nil
}

// Close closes the interactive session
func (s *SSHSession) Close() error {
	return s.session.Close()
}

// Write sends data to the session
func (s *SSHSession) Write(data []byte) (int, error) {
	return s.stdin.Write(data)
}

// Read reads data from the session
func (s *SSHSession) Read(data []byte) (int, error) {
	return s.stdout.Read(data)
}
