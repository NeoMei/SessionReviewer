param(
  [string]$Version = "0.3.2",
  [string]$Dist = "dist"
)

$ErrorActionPreference = "Stop"
$dirty = git status --porcelain=v1 --untracked-files=all
if ($dirty) {
  throw "build-release: working tree must be clean"
}

$commit = (git rev-parse HEAD).Trim()
$builtAt = (git show -s --format=%cI HEAD).Trim()

go run ./cmd/release-packager `
  --source . `
  --dist $Dist `
  --version $Version `
  --commit $commit `
  --built-at $builtAt
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

foreach ($artifact in @(
  (Join-Path $Dist "session-reviewer_${Version}_darwin_amd64.tar.gz"),
  (Join-Path $Dist "session-reviewer_${Version}_darwin_arm64.tar.gz"),
  (Join-Path $Dist "session-reviewer_${Version}_windows_amd64.zip")
)) {
  if ($artifact.EndsWith(".tar.gz")) {
    $entries = tar -tzf $artifact
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
  } else {
    $archive = [System.IO.Compression.ZipFile]::OpenRead($artifact)
    try {
      $entries = $archive.Entries.FullName
    } finally {
      $archive.Dispose()
    }
  }
  if ($entries -notcontains "session-reviewer/schemas/review-job-status-v1.schema.json") {
    throw "release archive omitted review-job-status-v1.schema.json: $artifact"
  }
}

Write-Output "release candidate: $Version $commit"
Write-Output "artifacts: $Dist"
