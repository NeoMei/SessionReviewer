param(
  [Parameter(Mandatory = $true)][string]$Version,
  [string]$Dist = "dist"
)

$ErrorActionPreference = "Stop"
if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "version must be semantic without a v prefix" }

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$PluginRoot = Join-Path $RepoRoot "obsidian-plugin"
$null = New-Item -ItemType Directory -Force $Dist
$DistRoot = (Resolve-Path $Dist).Path

Push-Location $PluginRoot
try {
  if ($env:SESSION_REVIEWER_PACKAGE_SKIP_CHECK -ne "1") {
    npm ci
    if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
    npm run check
    if ($LASTEXITCODE -ne 0) { throw "npm run check failed" }
  } else {
    npm run build
    if ($LASTEXITCODE -ne 0) { throw "npm run build failed" }
  }
  $Package = Get-Content package.json -Raw | ConvertFrom-Json
  $Manifest = Get-Content manifest.json -Raw | ConvertFrom-Json
  $Versions = Get-Content versions.json -Raw | ConvertFrom-Json
  if ($Package.version -ne $Version -or $Manifest.version -ne $Version -or $Versions.$Version -ne $Manifest.minAppVersion) {
    throw "package, manifest, and versions.json do not match requested version"
  }
  if ($Manifest.id -ne "session-reviewer") { throw "unexpected plugin ID" }
} finally {
  Pop-Location
}

$Archive = Join-Path $DistRoot "session-reviewer-obsidian-$Version.zip"
if (Test-Path $Archive) { Remove-Item -LiteralPath $Archive -Force }
$Epoch = 315532800L
if ($env:SOURCE_DATE_EPOCH) { $Epoch = [Math]::Max($Epoch, [Int64]::Parse($env:SOURCE_DATE_EPOCH)) }
$Timestamp = [DateTimeOffset]::FromUnixTimeSeconds($Epoch)

Add-Type -AssemblyName System.IO.Compression
$Stream = [System.IO.File]::Open($Archive, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
try {
  $Zip = [System.IO.Compression.ZipArchive]::new($Stream, [System.IO.Compression.ZipArchiveMode]::Create, $false)
  try {
    foreach ($Name in @("main.js", "manifest.json", "styles.css")) {
      $Entry = $Zip.CreateEntry("session-reviewer/$Name", [System.IO.Compression.CompressionLevel]::Optimal)
      $Entry.LastWriteTime = $Timestamp
      $Input = [System.IO.File]::OpenRead((Join-Path $PluginRoot $Name))
      $Output = $Entry.Open()
      try { $Input.CopyTo($Output) } finally { $Output.Dispose(); $Input.Dispose() }
    }
  } finally { $Zip.Dispose() }
} finally { $Stream.Dispose() }

foreach ($Name in @("main.js", "manifest.json", "styles.css")) {
  Copy-Item -LiteralPath (Join-Path $PluginRoot $Name) -Destination (Join-Path $DistRoot $Name) -Force
}
$Assets = @($Archive, (Join-Path $DistRoot "main.js"), (Join-Path $DistRoot "manifest.json"), (Join-Path $DistRoot "styles.css"))
$Checksums = $Assets | ForEach-Object {
  $Hash = (Get-FileHash -Algorithm SHA256 $_).Hash.ToLowerInvariant()
  "$Hash  $([System.IO.Path]::GetFileName($_))"
} | Sort-Object { ($_ -split '  ', 2)[1] }
(($Checksums -join "`n") + "`n") | Set-Content -NoNewline -Encoding utf8 (Join-Path $DistRoot "SHA256SUMS")
Write-Output $Archive
