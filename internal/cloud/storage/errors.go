package storage

import (
	"errors"
	"strings"
)

// Common storage operation errors
var (
	// ErrInsufficientSpace indicates there isn't enough disk space for the operation
	ErrInsufficientSpace = errors.New("insufficient disk space")
	// ErrChecksumMismatch indicates file integrity check failed
	ErrChecksumMismatch = errors.New("checksum mismatch")
	// ErrEncryptionFailed indicates encryption operation failed
	ErrEncryptionFailed = errors.New("encryption failed")
	// ErrDecryptionFailed indicates decryption operation failed
	ErrDecryptionFailed = errors.New("decryption failed")
	// ErrFileChanged indicates remote file changed during download
	ErrFileChanged = errors.New("remote file changed during operation")
)

// IsDiskFullError checks if an error is likely caused by running out of disk space
// This catches errors that occur during file operations when disk becomes full
//
// Checks for common error strings across different operating systems:
//   - Linux/Unix: "no space left on device", "enospc"
//   - Windows: "out of disk space", "insufficient disk space"
//   - Generic: "disk full", "not enough space"
//   - Quota: "disk quota exceeded"
func IsDiskFullError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())

	diskFullIndicators := []string{
		"no space left on device", // Linux/Unix
		"disk full",               // Generic
		"out of disk space",       // Windows
		"insufficient disk space", // Windows
		"not enough space",        // Generic
		"enospc",                  // Linux errno
		"disk quota exceeded",     // Quota systems
	}

	for _, indicator := range diskFullIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}

	return false
}

// IsNetworkError checks if an error is network-related
// Useful for determining if an operation should be retried
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())

	networkIndicators := []string{
		"connection",    // connection refused, connection reset, etc.
		"timeout",       // i/o timeout, dial timeout, etc.
		"network",       // network unreachable, network error, etc.
		"eof",           // unexpected EOF
		"broken pipe",   // broken pipe
		"tls handshake", // TLS handshake errors
	}

	for _, indicator := range networkIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}

	return false
}

// IsCredentialError checks if an error is authentication/authorization related
// Useful for determining if credentials need to be refreshed
func IsCredentialError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())

	credentialIndicators := []string{
		"403",           // HTTP Forbidden
		"unauthorized",  // HTTP Unauthorized
		"expired",       // expired token/credential
		"expiredtoken",  // AWS specific
		"invalid token", // invalid authentication
	}

	for _, indicator := range credentialIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}

	return false
}
