param(
  [string]$Version = "0.1.0",
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

Write-Output "release candidate: $Version $commit"
Write-Output "artifacts: $Dist"
