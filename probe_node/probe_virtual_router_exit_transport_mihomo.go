//go:build mihomo_exit

package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/txthinking/socks5"
)

const (
	probeMihomoExitSOCKSAddressEnv    = "PROBE_MIHOMO_SOCKS5_ADDRESS"
	probeMihomoExitSOCKSUsernameEnv   = "PROBE_MIHOMO_SOCKS5_USERNAME"
	probeMihomoExitSOCKSPasswordEnv   = "PROBE_MIHOMO_SOCKS5_PASSWORD"
	probeMihomoExitDesiredRevisionEnv = "PROBE_MIHOMO_DESIRED_REVISION"
	probeMihomoExitAppliedRevisionEnv = "PROBE_MIHOMO_APPLIED_REVISION"
	probeMihomoExitDesiredSHA256Env   = "PROBE_MIHOMO_DESIRED_SHA256"
	probeMihomoExitAppliedSHA256Env   = "PROBE_MIHOMO_APPLIED_SHA256"
	probeMihomoExitHealthyEnv         = "PROBE_MIHOMO_HEALTHY"
	probeMihomoExitPOCModeEnv         = "PROBE_MIHOMO_POC_MODE"
)

type probeMihomoExitRuntimeConfig struct {
	SOCKSAddress    string
	SOCKSUsername   string
	SOCKSPassword   string
	DesiredRevision int64
	AppliedRevision int64
	DesiredSHA256   string
	AppliedSHA256   string
	Healthy         bool
}

func loadProbeMihomoExitRuntimeConfig() (probeMihomoExitRuntimeConfig, error) {
	if config, ok := currentProbeMihomoRuntimeConfig(); ok {
		if err := validateProbeMihomoExitRuntimeConfig(config); err != nil {
			return probeMihomoExitRuntimeConfig{}, err
		}
		return config, nil
	}
	if !parseProbeBoolEnv(probeMihomoExitPOCModeEnv, false) {
		return probeMihomoExitRuntimeConfig{}, errors.New("mihomo managed runtime is not configured")
	}
	desiredRevision, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(probeMihomoExitDesiredRevisionEnv)), 10, 64)
	if err != nil || desiredRevision <= 0 {
		return probeMihomoExitRuntimeConfig{}, errors.New("mihomo desired revision is invalid")
	}
	appliedRevision, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(probeMihomoExitAppliedRevisionEnv)), 10, 64)
	if err != nil || appliedRevision <= 0 {
		return probeMihomoExitRuntimeConfig{}, errors.New("mihomo applied revision is invalid")
	}
	config := probeMihomoExitRuntimeConfig{
		SOCKSAddress:    strings.TrimSpace(os.Getenv(probeMihomoExitSOCKSAddressEnv)),
		SOCKSUsername:   strings.TrimSpace(os.Getenv(probeMihomoExitSOCKSUsernameEnv)),
		SOCKSPassword:   os.Getenv(probeMihomoExitSOCKSPasswordEnv),
		DesiredRevision: desiredRevision,
		AppliedRevision: appliedRevision,
		DesiredSHA256:   strings.ToLower(strings.TrimSpace(os.Getenv(probeMihomoExitDesiredSHA256Env))),
		AppliedSHA256:   strings.ToLower(strings.TrimSpace(os.Getenv(probeMihomoExitAppliedSHA256Env))),
		Healthy:         parseProbeBoolEnv(probeMihomoExitHealthyEnv, false),
	}
	if err := validateProbeMihomoExitRuntimeConfig(config); err != nil {
		return probeMihomoExitRuntimeConfig{}, err
	}
	return config, nil
}

func validateProbeMihomoExitRuntimeConfig(config probeMihomoExitRuntimeConfig) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(config.SOCKSAddress))
	if err != nil {
		return fmt.Errorf("mihomo socks address is invalid: %w", err)
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil || !ip.IsLoopback() {
		return errors.New("mihomo socks address must use a literal loopback IP")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber <= 0 || portNumber > 65535 {
		return errors.New("mihomo socks port is invalid")
	}
	if config.SOCKSUsername == "" || config.SOCKSPassword == "" {
		return errors.New("mihomo socks authentication is required")
	}
	if config.DesiredRevision <= 0 || config.DesiredRevision != config.AppliedRevision {
		return errors.New("mihomo desired and applied revisions do not match")
	}
	if !validProbeMihomoExitSHA256(config.DesiredSHA256) || config.DesiredSHA256 != config.AppliedSHA256 {
		return errors.New("mihomo desired and applied hashes do not match")
	}
	if !config.Healthy {
		return errors.New("mihomo runtime is not healthy")
	}
	return nil
}

func validProbeMihomoExitSHA256(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == 32
}

func dialProbeVirtualRouterProductExitTCP(target probeVirtualRouterExitTarget) (net.Conn, error) {
	return dialProbeMihomoExitSOCKS("tcp", target)
}

func dialProbeVirtualRouterProductExitUDP(target probeVirtualRouterExitTarget) (net.Conn, error) {
	return dialProbeMihomoExitSOCKS("udp", target)
}

func dialProbeMihomoExitSOCKS(network string, target probeVirtualRouterExitTarget) (net.Conn, error) {
	config, err := loadProbeMihomoExitRuntimeConfig()
	if err != nil {
		return nil, fmt.Errorf("mihomo exit is not ready: %w", err)
	}
	if target.Port == 0 || strings.TrimSpace(target.Host) == "" {
		return nil, errors.New("mihomo exit target is empty")
	}
	client, err := socks5.NewClient(config.SOCKSAddress, config.SOCKSUsername, config.SOCKSPassword, int(probeVirtualRouterExitDialTimeout/time.Second), int(probeVirtualRouterExitUDPIdleTimeout/time.Second))
	if err != nil {
		return nil, err
	}
	client.DialTCP = func(_ string, _ string, remote string) (net.Conn, error) {
		if remote != config.SOCKSAddress {
			return nil, errors.New("mihomo socks tcp dial escaped configured loopback endpoint")
		}
		return (&net.Dialer{Timeout: probeVirtualRouterExitDialTimeout}).Dial("tcp4", remote)
	}
	client.DialUDP = func(_ string, _ string, remote string) (net.Conn, error) {
		remoteAddr, err := net.ResolveUDPAddr("udp4", remote)
		if err != nil {
			return nil, err
		}
		if remoteAddr.IP == nil || !remoteAddr.IP.IsLoopback() {
			return nil, errors.New("mihomo socks udp relay is not loopback")
		}
		return net.DialUDP("udp4", nil, remoteAddr)
	}
	conn, err := client.Dial(strings.ToLower(strings.TrimSpace(network)), target.Address())
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	probeMihomoSessionOpened()
	return &probeMihomoTrackedConn{Conn: conn}, nil
}

type probeMihomoTrackedConn struct {
	net.Conn
	closeOnce sync.Once
}

func (conn *probeMihomoTrackedConn) Read(buffer []byte) (int, error) {
	count, err := conn.Conn.Read(buffer)
	probeMihomoBytesTransferred(false, count)
	return count, err
}

func (conn *probeMihomoTrackedConn) Write(buffer []byte) (int, error) {
	count, err := conn.Conn.Write(buffer)
	probeMihomoBytesTransferred(true, count)
	return count, err
}

func (conn *probeMihomoTrackedConn) Close() error {
	conn.closeOnce.Do(probeMihomoSessionClosed)
	return conn.Conn.Close()
}

func probeProductAllowsPhysicalICMPExit() bool {
	return false
}
