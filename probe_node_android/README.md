# CloudHelper Probe Node Android

## Release signing

CI builds the Android release APK in two stages:

1. Build an unsigned release APK without access to signing secrets.
2. Sign the APK in the protected GitHub Actions environment `android-release-signing`.

The protected environment must contain these secrets:

- `CLOUDHELPER_ANDROID_KEYSTORE_BASE64`
- `CLOUDHELPER_ANDROID_KEYSTORE_PASSWORD`

Generate a PKCS12 signing keystore:

```powershell
New-Item -ItemType Directory -Force .secrets/android | Out-Null
$rsa = [System.Security.Cryptography.RSA]::Create(4096)
$request = [System.Security.Cryptography.X509Certificates.CertificateRequest]::new(
    "CN=CloudHelper Probe Node Android Release",
    $rsa,
    [System.Security.Cryptography.HashAlgorithmName]::SHA256,
    [System.Security.Cryptography.RSASignaturePadding]::Pkcs1
)
$cert = $request.CreateSelfSigned([DateTimeOffset]::UtcNow.AddDays(-1), [DateTimeOffset]::UtcNow.AddYears(100))
$password = Read-Host -AsSecureString "Android signing keystore password"
$pfxBytes = $cert.CopyWithPrivateKey($rsa).Export([System.Security.Cryptography.X509Certificates.X509ContentType]::Pkcs12, $password)
[IO.File]::WriteAllBytes(".secrets/android/cloudhelper-probe-node-android-release.p12", $pfxBytes)
```

Encode the keystore for GitHub Environment Secrets:

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes(".secrets/android/cloudhelper-probe-node-android-release.p12"))
```

The local `.secrets/` directory is ignored by git. Keep an offline backup of the `.p12` file and password; Android upgrades require every future APK to be signed by the same key.
