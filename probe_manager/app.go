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
type ReleaseAsset = backend.ReleaseAsset
type ReleaseInfo = backend.ReleaseInfo
type ManagerUpgradeResult = backend.ManagerUpgradeResult
type ManagerUpgradeProgress = backend.ManagerUpgradeProgress
type ProbeNode = backend.ProbeNode

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

func (a *App) GetLocalManagerLogs(lines int, sinceMinutes int) (LogViewResponse, error) {
	return a.inner.GetLocalManagerLogs(lines, sinceMinutes)
}

func (a *App) GetNetworkAssistantStatus() NetworkAssistantStatus {
	return a.inner.GetNetworkAssistantStatus()
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

func (a *App) CreateProbeNode(nodeName string) (ProbeNode, error) {
	return a.inner.CreateProbeNode(nodeName)
}

func (a *App) UpdateProbeNode(nodeNo int, targetSystem string, directConnect bool) (ProbeNode, error) {
	return a.inner.UpdateProbeNode(nodeNo, targetSystem, directConnect)
}
