package selfupdate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"runtime"
	"syscall"
)

// FailureKind returns bounded categories, never paths, URLs or provider text.
func FailureKind(err error) string {
	var status *downloadHTTPError
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	var unknownCA x509.UnknownAuthorityError
	var invalidCertificate x509.CertificateInvalidError
	var hostname x509.HostnameError
	var tlsRecord tls.RecordHeaderError
	var timeout net.Error
	var network *net.OpError
	var request *url.Error
	switch {
	case errors.As(err, &status):
		return fmt.Sprintf("http_%d", status.status)
	case errors.Is(err, context.DeadlineExceeded), os.IsTimeout(err), errors.As(err, &timeout) && timeout.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.As(err, &dns):
		return "dns_error"
	case errors.As(err, &certificate), errors.As(err, &unknownCA), errors.As(err, &invalidCertificate), errors.As(err, &hostname), errors.As(err, &tlsRecord):
		return "tls_error"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, syscall.ENOSPC):
		return "disk_full"
	case errors.Is(err, syscall.EBUSY), errors.Is(err, syscall.ETXTBSY):
		return "file_in_use"
	case errors.Is(err, syscall.EROFS):
		return "read_only"
	case errors.Is(err, syscall.EXDEV):
		return "cross_device"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "transfer_incomplete"
	case errors.Is(err, syscall.ENOTEMPTY):
		return "directory_not_empty"
	case errors.As(err, &network), errors.As(err, &request):
		return "network_error"
	}
	if runtime.GOOS == "windows" {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			switch errno {
			case 32, 33: // ERROR_SHARING_VIOLATION, ERROR_LOCK_VIOLATION
				return "file_in_use"
			case 39, 112: // ERROR_HANDLE_DISK_FULL, ERROR_DISK_FULL
				return "disk_full"
			}
		}
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("os_%s_%d", runtime.GOOS, errno)
	}
	return "failed"
}

type downloadHTTPError struct{ status int }

func (e *downloadHTTPError) Error() string { return fmt.Sprintf("CLI download HTTP %d", e.status) }

type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func atStep(step string, err error) error { return &stepError{step: step, err: err} }

func failureCode(phase string, err error) string {
	var step *stepError
	if errors.As(err, &step) {
		phase += "_" + step.step
	}
	return phase + "_" + FailureKind(err)
}
