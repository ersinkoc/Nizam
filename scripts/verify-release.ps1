param(
    [Parameter(Mandatory = $true)]
    [string]$Tag,

    [string]$Repository = "ersinkoc/Mizan",

    [string]$OutputDirectory = "",

    [switch]$SkipDownload,

    [switch]$VerifySignatures,

    [string]$CosignPath = "",

    [string]$CertificateIdentity = "",

    [string]$CertificateOidcIssuer = "https://token.actions.githubusercontent.com"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-Executable {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name,

        [string]$Fallback = ""
    )

    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($command) {
        return $command.Source
    }
    if ($Fallback -and (Test-Path -LiteralPath $Fallback)) {
        return $Fallback
    }
    throw "Required executable not found: $Name"
}

if (-not $OutputDirectory) {
    $OutputDirectory = Join-Path (Join-Path $PSScriptRoot "..\dist\release-verify") $Tag
}

$gh = Resolve-Executable -Name "gh" -Fallback "C:\Program Files\GitHub CLI\gh.exe"
$cosign = ""
if ($VerifySignatures) {
    if ($CosignPath) {
        if (-not (Test-Path -LiteralPath $CosignPath -PathType Leaf)) {
            throw "Cosign executable not found: $CosignPath"
        }
        $cosign = $CosignPath
    }
    else {
        $cosign = Resolve-Executable -Name "cosign"
    }
    if (-not $CertificateIdentity) {
        $CertificateIdentity = "https://github.com/$Repository/.github/workflows/release.yml@refs/tags/$Tag"
    }
}
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

if (-not $SkipDownload) {
    & $gh release download $Tag --repo $Repository --dir $OutputDirectory --clobber
}

$platforms = @(
    "darwin-amd64",
    "darwin-arm64",
    "linux-amd64",
    "linux-arm64",
    "windows-amd64"
)

$verified = @()

foreach ($platform in $platforms) {
    $baseName = "mizan-$platform"
    $binaryPath = Join-Path $OutputDirectory $baseName
    $checksumPath = "$binaryPath.sha256"
    $signaturePath = "$binaryPath.sig"
    $certificatePath = "$binaryPath.pem"

    foreach ($requiredPath in @($binaryPath, $checksumPath, $signaturePath, $certificatePath)) {
        if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
            throw "Missing release asset: $requiredPath"
        }
    }

    $expectedLine = Get-Content -LiteralPath $checksumPath -TotalCount 1
    $expectedHash = (($expectedLine -split "\s+")[0]).ToLowerInvariant()
    $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binaryPath).Hash.ToLowerInvariant()

    if ($actualHash -ne $expectedHash) {
        throw "SHA-256 mismatch for $baseName. expected=$expectedHash actual=$actualHash"
    }

    if ($VerifySignatures) {
        & $cosign verify-blob `
            --certificate $certificatePath `
            --signature $signaturePath `
            --certificate-identity $CertificateIdentity `
            --certificate-oidc-issuer $CertificateOidcIssuer `
            $binaryPath
        if ($LASTEXITCODE -ne 0) {
            throw "Cosign verification failed for $baseName"
        }
    }

    $verified += [pscustomobject]@{
        Asset  = $baseName
        SHA256 = $actualHash
    }
}

$assetCount = (Get-ChildItem -LiteralPath $OutputDirectory -File).Count
if ($assetCount -ne 20) {
    throw "Expected 20 release assets, found $assetCount in $OutputDirectory"
}

$verified | Format-Table -AutoSize
Write-Host "Release verification passed for ${Repository}@${Tag}: $($verified.Count) binaries, $assetCount assets."
if ($VerifySignatures) {
    Write-Host "Sigstore verification passed with identity '$CertificateIdentity' and issuer '$CertificateOidcIssuer'."
}
