package plan

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SSHKeyInfo holds information about an SSH key pair
type SSHKeyInfo struct {
	KeyPairName    string
	PrivateKeyPath string
	PublicKeyPath  string
	Fingerprint    string
	ExistsInAWS    bool
	ExistsLocally  bool
}

// EnsureSSHKey ensures an SSH key pair exists both locally and in AWS
func EnsureSSHKey(ctx context.Context, keyPairName, region, profile string, w io.Writer) (*SSHKeyInfo, error) {
	progress := NewProgressWriter(w, 3, false)

	info := &SSHKeyInfo{
		KeyPairName: keyPairName,
	}

	// Step 1: Check if key pair exists in AWS
	progress.LogNote(fmt.Sprintf("checking for SSH key pair '%s' in AWS", keyPairName))

	awsKey, err := describeKeyPair(ctx, keyPairName, region, profile)
	if err == nil && awsKey != "" {
		info.ExistsInAWS = true
		info.Fingerprint = awsKey
		progress.LogNote(fmt.Sprintf("key pair '%s' exists in AWS", keyPairName))
	} else {
		progress.LogNote(fmt.Sprintf("key pair '%s' not found in AWS", keyPairName))
	}

	// Step 2: Check for local SSH key
	homeDir, _ := os.UserHomeDir()
	defaultKeyPath := filepath.Join(homeDir, ".ssh", "id_rsa")
	customKeyPath := filepath.Join(homeDir, ".ssh", keyPairName)

	// Check custom key first, then default
	for _, keyPath := range []string{customKeyPath, customKeyPath + ".pem", defaultKeyPath} {
		if _, err := os.Stat(keyPath); err == nil {
			info.PrivateKeyPath = keyPath
			info.PublicKeyPath = keyPath + ".pub"
			info.ExistsLocally = true
			progress.LogNote(fmt.Sprintf("found local SSH key at %s", keyPath))
			break
		}
	}

	// Step 3: Generate key if needed
	if !info.ExistsLocally {
		progress.LogNote("generating new SSH key pair")
		keyPath := filepath.Join(homeDir, ".ssh", keyPairName)
		if err := generateSSHKeyPair(keyPath); err != nil {
			return nil, fmt.Errorf("failed to generate SSH key: %w", err)
		}
		info.PrivateKeyPath = keyPath
		info.PublicKeyPath = keyPath + ".pub"
		info.ExistsLocally = true
		progress.LogNote(fmt.Sprintf("generated SSH key pair at %s", keyPath))
	}

	// Step 4: Import to AWS if needed
	if !info.ExistsInAWS && info.ExistsLocally {
		progress.LogNote(fmt.Sprintf("importing SSH key '%s' to AWS", keyPairName))
		if err := importKeyPairToAWS(ctx, keyPairName, info.PublicKeyPath, region, profile); err != nil {
			return nil, fmt.Errorf("failed to import SSH key to AWS: %w", err)
		}
		info.ExistsInAWS = true
		progress.LogNote(fmt.Sprintf("imported SSH key '%s' to AWS", keyPairName))
	}

	return info, nil
}

// describeKeyPair checks if a key pair exists in AWS
func describeKeyPair(ctx context.Context, keyPairName, region, profile string) (string, error) {
	args := []string{
		"ec2", "describe-key-pairs",
		"--key-names", keyPairName,
		"--query", "KeyPairs[0].KeyFingerprint",
		"--output", "text",
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}

	fingerprint := strings.TrimSpace(string(out))
	if fingerprint == "" || fingerprint == "None" {
		return "", fmt.Errorf("key pair not found")
	}

	return fingerprint, nil
}

// generateSSHKeyPair generates a new RSA key pair
func generateSSHKeyPair(keyPath string) error {
	// Ensure .ssh directory exists
	sshDir := filepath.Dir(keyPath)
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return err
	}

	// Generate RSA key
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return err
	}

	// Write private key
	privateKeyFile, err := os.OpenFile(keyPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer privateKeyFile.Close()

	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}
	if err := pem.Encode(privateKeyFile, privateKeyPEM); err != nil {
		return err
	}

	// Generate public key
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return err
	}

	// Write public key
	publicKeyBytes := ssh.MarshalAuthorizedKey(publicKey)
	if err := os.WriteFile(keyPath+".pub", publicKeyBytes, 0644); err != nil {
		return err
	}

	return nil
}

// importKeyPairToAWS imports a public key to AWS EC2
func importKeyPairToAWS(ctx context.Context, keyPairName, publicKeyPath, region, profile string) error {
	// Read public key
	pubKeyBytes, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return err
	}

	args := []string{
		"ec2", "import-key-pair",
		"--key-name", keyPairName,
		"--public-key-material", fmt.Sprintf("fileb://%s", publicKeyPath),
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Check if key already exists
		if strings.Contains(string(out), "InvalidKeyPair.Duplicate") {
			return nil
		}
		return fmt.Errorf("import failed: %s", string(out))
	}

	_ = pubKeyBytes // unused but read for validation

	return nil
}

// DeleteKeyPair deletes a key pair from AWS
func DeleteKeyPair(ctx context.Context, keyPairName, region, profile string) error {
	args := []string{
		"ec2", "delete-key-pair",
		"--key-name", keyPairName,
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "InvalidKeyPair.NotFound") {
			return nil
		}
		return fmt.Errorf("delete failed: %s", string(out))
	}

	return nil
}

// CreateKeyPairInAWS creates a new key pair in AWS and saves the private key locally
func CreateKeyPairInAWS(ctx context.Context, keyPairName, region, profile string, w io.Writer) (*SSHKeyInfo, error) {
	progress := NewProgressWriter(w, 2, false)

	homeDir, _ := os.UserHomeDir()
	keyPath := filepath.Join(homeDir, ".ssh", keyPairName+".pem")

	// Create key pair in AWS
	progress.LogNote(fmt.Sprintf("creating SSH key pair '%s' in AWS", keyPairName))

	args := []string{
		"ec2", "create-key-pair",
		"--key-name", keyPairName,
		"--query", "KeyMaterial",
		"--output", "text",
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to create key pair: %s", string(out))
	}

	// Save private key locally
	sshDir := filepath.Dir(keyPath)
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return nil, err
	}

	if err := os.WriteFile(keyPath, out, 0600); err != nil {
		return nil, err
	}

	progress.LogNote(fmt.Sprintf("saved private key to %s", keyPath))

	return &SSHKeyInfo{
		KeyPairName:    keyPairName,
		PrivateKeyPath: keyPath,
		ExistsInAWS:    true,
		ExistsLocally:  true,
	}, nil
}

// ListKeyPairs lists all key pairs in AWS
func ListKeyPairs(ctx context.Context, region, profile string) ([]string, error) {
	args := []string{
		"ec2", "describe-key-pairs",
		"--query", "KeyPairs[*].KeyName",
		"--output", "json",
		"--profile", profile,
		"--region", region,
		"--no-cli-pager",
	}

	cmd := exec.CommandContext(ctx, "aws", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	var keyNames []string
	if err := json.Unmarshal(out, &keyNames); err != nil {
		return nil, err
	}

	return keyNames, nil
}

// ValidateSSHKey validates that an SSH private key is valid
func ValidateSSHKey(keyPath string) error {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read key file: %w", err)
	}

	_, err = ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		return fmt.Errorf("invalid SSH private key: %w", err)
	}

	return nil
}

// GetSSHKeyPath returns the path to an SSH key, checking common locations
func GetSSHKeyPath(keyPairName string) string {
	homeDir, _ := os.UserHomeDir()

	// Check common locations
	paths := []string{
		filepath.Join(homeDir, ".ssh", keyPairName),
		filepath.Join(homeDir, ".ssh", keyPairName+".pem"),
		filepath.Join(homeDir, ".ssh", "id_rsa"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Return default path
	return filepath.Join(homeDir, ".ssh", keyPairName)
}
