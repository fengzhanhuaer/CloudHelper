//go:build mihomo_exit || linux_router

package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type probeMihomoUpgradeManifest struct {
	SchemaVersion             int    `json:"schema_version"`
	Version                   string `json:"version"`
	BuildKind                 string `json:"build_kind"`
	OS                        string `json:"os"`
	Arch                      string `json:"arch"`
	CompatibleProgramVersions struct {
		Min string `json:"min"`
		Max string `json:"max"`
	} `json:"compatible_program_versions"`
	Program struct {
		Asset  string `json:"asset"`
		SHA256 string `json:"sha256"`
	} `json:"program"`
	Mihomo struct {
		Version string `json:"version"`
		Asset   string `json:"asset"`
		URL     string `json:"url"`
		SHA256  string `json:"sha256"`
	} `json:"mihomo"`
}

func prepareProbeProductUpgradeCompanion(ctx context.Context, mode string, release releaseInfo, controllerBase string, identity nodeIdentity, workDir string, programAssetPath string) (probeProductUpgradeCompanion, error) {
	manifestAssetName, err := currentProbeMihomoUpgradeManifestAsset()
	if err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	manifestAsset, err := findProbeUpgradeAsset(release.Assets, manifestAssetName)
	if err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	manifestPath := filepath.Join(workDir, manifestAssetName)
	if err = downloadProbeAsset(ctx, mode, manifestAsset.DownloadURL, controllerBase, identity, manifestPath, nil); err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	var manifest probeMihomoUpgradeManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return probeProductUpgradeCompanion{}, fmt.Errorf("invalid paired upgrade manifest: %w", err)
	}
	if err = validateProbeMihomoUpgradeManifest(manifest, release.TagName); err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	if err = verifyProbeUpgradeFileSHA256(programAssetPath, manifest.Program.SHA256); err != nil {
		return probeProductUpgradeCompanion{}, fmt.Errorf("program asset: %w", err)
	}
	archivePath := filepath.Join(workDir, filepath.Base(manifest.Mihomo.Asset))
	if err = downloadProbeAsset(ctx, mode, manifest.Mihomo.URL, controllerBase, identity, archivePath, nil); err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	if err = verifyProbeUpgradeFileSHA256(archivePath, manifest.Mihomo.SHA256); err != nil {
		return probeProductUpgradeCompanion{}, fmt.Errorf("mihomo asset: %w", err)
	}
	candidatePath := filepath.Join(workDir, "mihomo")
	if err = extractProbeMihomoGzip(archivePath, candidatePath); err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	if err = verifyProbeMihomoUpgradeCandidate(candidatePath); err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	dataDir, err := resolveDataDir()
	if err != nil {
		return probeProductUpgradeCompanion{}, err
	}
	return probeProductUpgradeCompanion{CandidatePath: candidatePath, TargetPath: filepath.Join(dataDir, probeMihomoBinaryFileName)}, nil
}

func validateProbeMihomoUpgradeManifest(manifest probeMihomoUpgradeManifest, releaseTag string) error {
	if manifest.SchemaVersion != 1 {
		return fmt.Errorf("unsupported paired upgrade manifest schema %d", manifest.SchemaVersion)
	}
	expectedKind := currentProbeBuildKind()
	expectedArch := runtime.GOARCH
	if manifest.BuildKind != expectedKind || manifest.OS != "linux" || manifest.Arch != expectedArch {
		return errors.New("paired upgrade manifest build target mismatch")
	}
	if normalizeVersionTag(manifest.Version) != normalizeVersionTag(releaseTag) {
		return errors.New("paired upgrade manifest release version mismatch")
	}
	if normalizeVersionTag(manifest.CompatibleProgramVersions.Min) != normalizeVersionTag(manifest.Version) || normalizeVersionTag(manifest.CompatibleProgramVersions.Max) != normalizeVersionTag(manifest.Version) {
		return errors.New("paired upgrade compatibility range does not include exactly the target release")
	}
	expectedProgramAsset := activeProbeProductProfile.UpgradeAssetPrefix + "-linux-" + expectedArch
	if manifest.Program.Asset != expectedProgramAsset || !validProbeMihomoExitSHA256(manifest.Program.SHA256) {
		return errors.New("paired upgrade program metadata is invalid")
	}
	if !strings.HasPrefix(manifest.Mihomo.URL, "https://github.com/MetaCubeX/mihomo/releases/download/") || !strings.HasSuffix(manifest.Mihomo.Asset, ".gz") || !validProbeMihomoExitSHA256(manifest.Mihomo.SHA256) {
		return errors.New("paired upgrade mihomo metadata is invalid")
	}
	return nil
}

func currentProbeMihomoUpgradeManifestAsset() (string, error) {
	if currentProbeBuildKind() == probeBuildKindMihomoExit {
		return "cloudhelper-probe-exit-node-manifest.json", nil
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", fmt.Errorf("unsupported router upgrade architecture %s", runtime.GOARCH)
	}
	return activeProbeProductProfile.UpgradeAssetPrefix + "-linux-" + runtime.GOARCH + "-manifest.json", nil
}

func findProbeUpgradeAsset(assets []releaseAsset, name string) (releaseAsset, error) {
	for _, asset := range assets {
		if strings.TrimSpace(asset.Name) == name {
			return asset, nil
		}
	}
	return releaseAsset{}, fmt.Errorf("release asset %q not found", name)
}
func verifyProbeUpgradeFileSHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != strings.ToLower(strings.TrimSpace(expected)) {
		return fmt.Errorf("sha256 mismatch expected=%s actual=%s", expected, actual)
	}
	return nil
}
func extractProbeMihomoGzip(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer reader.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, reader)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(target, 0o755)
}
func verifyProbeMihomoUpgradeCandidate(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "-v").CombinedOutput()
	if err != nil {
		return fmt.Errorf("mihomo candidate verify failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(strings.ToLower(string(output)), "mihomo") {
		return errors.New("mihomo candidate version output is invalid")
	}
	return nil
}

func replaceProbeProductUpgradeCompanion(value probeProductUpgradeCompanion) (probeProductUpgradeCompanion, error) {
	if value.CandidatePath == "" {
		return value, errors.New("mihomo candidate path is empty")
	}
	mode := os.FileMode(0o755)
	if info, err := os.Stat(value.TargetPath); err == nil {
		mode = info.Mode().Perm()
	}
	newPath := value.TargetPath + ".new"
	if err := copyFileWithMode(value.CandidatePath, newPath, mode); err != nil {
		return value, err
	}
	backup := value.TargetPath + ".bak"
	_ = os.Remove(backup)
	if _, err := os.Stat(value.TargetPath); err == nil {
		if err = os.Rename(value.TargetPath, backup); err != nil {
			_ = os.Remove(newPath)
			return value, err
		}
		value.BackupPath = backup
	}
	if err := os.Rename(newPath, value.TargetPath); err != nil {
		if value.BackupPath != "" {
			_ = os.Rename(value.BackupPath, value.TargetPath)
		}
		return value, err
	}
	return value, nil
}

func rollbackProbeProductUpgradeCompanion(value probeProductUpgradeCompanion) error {
	if value.TargetPath == "" {
		return nil
	}
	if value.BackupPath == "" {
		return os.Remove(value.TargetPath)
	}
	failed := value.TargetPath + ".failed-" + time.Now().UTC().Format("20060102T150405")
	if _, err := os.Stat(value.TargetPath); err == nil {
		if err = os.Rename(value.TargetPath, failed); err != nil {
			return err
		}
	}
	return os.Rename(value.BackupPath, value.TargetPath)
}
