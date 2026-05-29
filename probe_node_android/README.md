# CloudHelper Probe Node Android

## Controller configuration

The Android MVP uses a WebView UI and a Kotlin system bridge. Probe protocol logic lives in Go `probe_node/mobilecore`.
The UI is organized as a two-pane console: project entries on the left, selected project details on the right. On narrow screens the entries collapse into a top navigation row.

The app stores controller connection settings locally:

- `Controller URL`: controller base URL, for example `https://controller.example.com`
- `Node ID`: probe node id issued by the controller
- `Node Secret`: probe node secret issued by the controller

Open the app, fill the three fields, and tap `Save`. Tap `Start` to call Go mobilecore. The MVP connects to `/api/probe`, authenticates with the same HMAC headers as desktop probe node, opens the yamux report stream, and sends Android platform reports.

GitHub Actions builds `probe_node/mobilecore` into `probe_node_android/app/libs/mobilecore.aar` with `gomobile bind` before assembling the APK. If the AAR is absent in a local shell build, the UI still opens and reports `mobilecore AAR is not packaged`.

Upgrade buttons follow the desktop probe node model:

- `Direct Upgrade`: fetches GitHub Releases directly.
- `Proxy Upgrade`: fetches `/api/probe/proxy/github/latest` and `/api/probe/proxy/download` through the configured controller with probe HMAC headers.

## Release signing

CI builds the Android release APK in two stages:

1. Build an unsigned release APK without access to signing secrets.
2. Sign the APK in the protected GitHub Actions environment `android-release-signing`.

The protected environment must contain these secrets:

- `CLOUDHELPER_ANDROID_KEYSTORE_BASE64`
- `CLOUDHELPER_ANDROID_KEYSTORE_PASSWORD`

Recommended environment protection:

- Enable required reviewers for `android-release-signing`.
- Keep secrets only in the environment, not repository-wide secrets.
- Do not upload local `.secrets/` files as workflow artifacts.

Generate a PKCS12 signing keystore:

```powershell
$secretDir = Join-Path (Get-Location) ".secrets/android"
New-Item -ItemType Directory -Force $secretDir | Out-Null
$p12Path = Join-Path $secretDir "cloudhelper-probe-node-android-release.p12"
$passwordPath = Join-Path $secretDir "cloudhelper-probe-node-android-release.password.txt"
$bytes = New-Object byte[] 36
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes)
$password = [Convert]::ToBase64String($bytes).TrimEnd("=")
$securePassword = ConvertTo-SecureString $password -AsPlainText -Force
$cert = New-SelfSignedCertificate -Subject "CN=CloudHelper Probe Node Android Release" -KeyAlgorithm RSA -KeyLength 4096 -HashAlgorithm SHA256 -CertStoreLocation "Cert:\CurrentUser\My" -NotAfter (Get-Date).AddYears(100) -KeyExportPolicy Exportable -KeyUsage DigitalSignature
try {
    Export-PfxCertificate -Cert $cert -FilePath $p12Path -Password $securePassword | Out-Null
    [IO.File]::WriteAllText($passwordPath, $password, [Text.Encoding]::ASCII)
} finally {
    if ($cert -and (Test-Path "Cert:\CurrentUser\My\$($cert.Thumbprint)")) {
        Remove-Item "Cert:\CurrentUser\My\$($cert.Thumbprint)" -Force
    }
}
```

Encode the keystore for GitHub Environment Secrets:

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes(".secrets/android/cloudhelper-probe-node-android-release.p12"))
```

The local `.secrets/` directory is ignored by git. Keep an offline backup of the `.p12` file and password; Android upgrades require every future APK to be signed by the same key.
