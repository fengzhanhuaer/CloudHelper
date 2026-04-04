package main

import (
	"context"

	"github.com/cloudhelper/probe_manager/backend"
)

var BuildVersion = "dev"

type App struct {
	inner *backend.App
}

type PrivateKeyStatus = backend.PrivateKeyStatus
type LogViewResponse = backend.LogViewResponse
type NetworkAssistantStatus = backend.NetworkAssistantStatus
type NetworkAssistantLogResponse = backend.NetworkAssistantLogResponse
type NetworkAssistantRuleGroupConfig = backend.NetworkAssistantRuleGroupConfig
type NetworkAssistantRuleConfig = backend.NetworkAssistantRuleConfig
type ReleaseAsset = backend.ReleaseAsset
type ReleaseInfo = backend.ReleaseInfo
type ManagerUpgradeResult = backend.ManagerUpgradeResult
type ManagerUpgradeProgress = backend.ManagerUpgradeProgress
type ProbeNode = backend.ProbeNode
type ProbeLinkConnectResult = backend.ProbeLinkConnectResult
type ProbeChainPingResult = backend.ProbeChainPingResult
type ProbeLinkChainCacheItem = backend.ProbeLinkChainCacheItem
type NetworkAssistantDNSUpstreamConfig = backend.NetworkAssistantDNSUpstreamConfig
type NetworkAssistantDNSCacheEntry = backend.NetworkAssistantDNSCacheEntry
type NetworkProcessInfo = backend.NetworkProcessInfo
type NetworkProcessEvent = backend.NetworkProcessEvent
type CloudflareSpeedTestRequest = backend.CloudflareSpeedTestRequest
type CloudflareSpeedTestResponse = backend.CloudflareSpeedTestResponse
type CloudflareIPTestResult = backend.CloudflareIPTestResult

func NewApp() *App {
	backend.BuildVersion = BuildVersion
	return &App{inner: backend.NewApp()}
}

func (a *App) startup(ctx context.Context) {
	a.inner.Startup(ctx)
}

func (a *App) shutdown(ctx context.Context) {
	a.inner.Shutdown(ctx)
}

func (a *App) Greet(name string) string {
	return a.inner.Greet(name)
}

func (a *App) GetManagerVersion() string {
	return a.inner.GetManagerVersion()
}

func (a *App) GetLocalPrivateKeyStatus() PrivateKeyStatus {
	return a.inner.GetLocalPrivateKeyStatus()
}

func (a *App) SignNonceWithLocalKey(nonce string) (string, error) {
	return a.inner.SignNonceWithLocalKey(nonce)
}

func (a *App) GetGlobalControllerURL() (string, error) {
	return a.inner.GetGlobalControllerURL()
}

func (a *App) SetGlobalControllerURL(rawURL string) (string, error) {
	return a.inner.SetGlobalControllerURL(rawURL)
}

func (a *App) GetGlobalControllerIP() (string, error) {
	return a.inner.GetGlobalControllerIP()
}

func (a *App) SetGlobalControllerIP(ip string) (string, error) {
	return a.inner.SetGlobalControllerIP(ip)
}

func (a *App) GetAIDebugListenEnabled() (bool, error) {
	return a.inner.GetAIDebugListenEnabled()
}

func (a *App) SetAIDebugListenEnabled(enabled bool) (bool, error) {
	return a.inner.SetAIDebugListenEnabled(enabled)
}

func (a *App) CloudflareSpeedTest(req CloudflareSpeedTestRequest) CloudflareSpeedTestResponse {
	return a.inner.CloudflareSpeedTest(req)
}

func (a *App) GetLocalManagerLogs(lines int, sinceMinutes int, minLevel string) (LogViewResponse, error) {
	return a.inner.GetLocalManagerLogs(lines, sinceMinutes, minLevel)
}

func (a *App) GetNetworkAssistantStatus() NetworkAssistantStatus {
	return a.inner.GetNetworkAssistantStatus()
}

func (a *App) GetNetworkAssistantLogs(lines int) (NetworkAssistantLogResponse, error) {
	return a.inner.GetNetworkAssistantLogs(lines)
}

func (a *App) SetNetworkAssistantMode(controllerBaseURL, sessionToken, mode, nodeID string) (NetworkAssistantStatus, error) {
	return a.inner.SetNetworkAssistantMode(controllerBaseURL, sessionToken, mode, nodeID)
}

func (a *App) SyncNetworkAssistant(controllerBaseURL, sessionToken string) (NetworkAssistantStatus, error) {
	return a.inner.SyncNetworkAssistant(controllerBaseURL, sessionToken)
}

func (a *App) RestoreNetworkAssistantDirect() (NetworkAssistantStatus, error) {
	return a.inner.RestoreNetworkAssistantDirect()
}

func (a *App) InstallNetworkAssistantTUN() (NetworkAssistantStatus, error) {
	return a.inner.InstallNetworkAssistantTUN()
}

func (a *App) EnableNetworkAssistantTUN() (NetworkAssistantStatus, error) {
	return a.inner.EnableNetworkAssistantTUN()
}

func (a *App) GetNetworkAssistantRuleConfig() (NetworkAssistantRuleConfig, error) {
	return a.inner.GetNetworkAssistantRuleConfig()
}

func (a *App) GetNetworkAssistantDNSUpstreamConfig() (NetworkAssistantDNSUpstreamConfig, error) {
	return a.inner.GetNetworkAssistantDNSUpstreamConfig()
}

func (a *App) SetNetworkAssistantDNSUpstreamConfig(cfg NetworkAssistantDNSUpstreamConfig) error {
	return a.inner.SetNetworkAssistantDNSUpstreamConfig(cfg)
}

func (a *App) ListNetworkAssistantProcesses() ([]NetworkProcessInfo, error) {
	return a.inner.ListNetworkAssistantProcesses()
}

func (a *App) StartNetworkAssistantProcessMonitor() error {
	return a.inner.StartNetworkAssistantProcessMonitor()
}

func (a *App) StopNetworkAssistantProcessMonitor() error {
	return a.inner.StopNetworkAssistantProcessMonitor()
}

func (a *App) ClearNetworkAssistantProcessEvents() error {
	return a.inner.ClearNetworkAssistantProcessEvents()
}

func (a *App) QueryNetworkAssistantProcessEvents(sinceMs int64) ([]NetworkProcessEvent, error) {
	return a.inner.QueryNetworkAssistantProcessEvents(sinceMs)
}

func (a *App) QueryNetworkAssistantDNSCache(query string) ([]NetworkAssistantDNSCacheEntry, error) {
	return a.inner.QueryNetworkAssistantDNSCache(query)
}

func (a *App) SetNetworkAssistantRulePolicy(group, action, tunnelNodeID string) (NetworkAssistantRuleConfig, error) {
	return a.inner.SetNetworkAssistantRulePolicy(group, action, tunnelNodeID)
}

func (a *App) ForceRefreshProbeDNSCache(controllerBaseURL, sessionToken string) (string, error) {
	return a.inner.ForceRefreshProbeDNSCache(controllerBaseURL, sessionToken)
}

func (a *App) ForceRefreshNetworkAssistantNodes(controllerBaseURL, sessionToken string) error {
	return a.inner.ForceRefreshNetworkAssistantNodes(controllerBaseURL, sessionToken)
}

func (a *App) UploadNetworkAssistantRuleRoutes(controllerBaseURL, sessionToken string) (string, error) {
	return a.inner.UploadNetworkAssistantRuleRoutes(controllerBaseURL, sessionToken)
}

func (a *App) GetLatestGitHubRelease(project string) (ReleaseInfo, error) {
	return a.inner.GetLatestGitHubRelease(project)
}

func (a *App) GetLatestGitHubReleaseViaProxy(controllerBaseURL, sessionToken, project string) (ReleaseInfo, error) {
	return a.inner.GetLatestGitHubReleaseViaProxy(controllerBaseURL, sessionToken, project)
}

func (a *App) UpgradeManagerDirect(project string) (ManagerUpgradeResult, error) {
	return a.inner.UpgradeManagerDirect(project)
}

func (a *App) UpgradeManagerViaProxy(controllerBaseURL, sessionToken, project string) (ManagerUpgradeResult, error) {
	return a.inner.UpgradeManagerViaProxy(controllerBaseURL, sessionToken, project)
}

func (a *App) GetManagerUpgradeProgress() ManagerUpgradeProgress {
	return a.inner.GetManagerUpgradeProgress()
}

func (a *App) GetProbeNodes() ([]ProbeNode, error) {
	return a.inner.GetProbeNodes()
}

func (a *App) GetProbeLinkChainsCache() ([]ProbeLinkChainCacheItem, error) {
	return a.inner.GetProbeLinkChainsCache()
}

func (a *App) CreateProbeNode(nodeName string) (ProbeNode, error) {
	return a.inner.CreateProbeNode(nodeName)
}

func (a *App) UpdateProbeNode(nodeNo int, targetSystem string, directConnect bool) (ProbeNode, error) {
	return a.inner.UpdateProbeNode(nodeNo, targetSystem, directConnect)
}

func (a *App) UpdateProbeNodeSettings(
	nodeNo int,
	nodeName string,
	remark string,
	targetSystem string,
	directConnect bool,
	paymentCycle string,
	cost string,
	expireAt string,
	vendorName string,
	vendorURL string,
) (ProbeNode, error) {
	return a.inner.UpdateProbeNodeSettings(
		nodeNo,
		nodeName,
		remark,
		targetSystem,
		directConnect,
		paymentCycle,
		cost,
		expireAt,
		vendorName,
		vendorURL,
	)
}

func (a *App) ReplaceProbeNodes(nodes []ProbeNode) ([]ProbeNode, error) {
	return a.inner.ReplaceProbeNodes(nodes)
}

func (a *App) TestProbeLink(nodeID, endpointType, scheme, host string, port int) (ProbeLinkConnectResult, error) {
	return a.inner.TestProbeLink(nodeID, endpointType, scheme, host, port)
}

func (a *App) StartProbeLinkSession(nodeID, protocol, host string, port int) (ProbeLinkConnectResult, error) {
	return a.inner.StartProbeLinkSession(nodeID, protocol, host, port)
}

func (a *App) PingProbeLinkSession() (ProbeLinkConnectResult, error) {
	return a.inner.PingProbeLinkSession()
}

func (a *App) StopProbeLinkSession() (bool, error) {
	return a.inner.StopProbeLinkSession()
}

func (a *App) PingProbeChain(chainID string) (ProbeChainPingResult, error) {
	return a.inner.PingProbeChain(chainID)
}

func (a *App) GetDeletedProbeNodeNos() ([]int, error) {
	return a.inner.GetDeletedProbeNodeNos()
}

func (a *App) MarkProbeNodeDeleted(nodeNo int) error {
	return a.inner.MarkProbeNodeDeleted(nodeNo)
}

func (a *App) RestoreDeletedProbeNode(nodeNo int) error {
	return a.inner.RestoreDeletedProbeNode(nodeNo)
}
