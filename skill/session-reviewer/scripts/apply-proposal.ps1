param(
    [Parameter(Mandatory = $true, Position = 0)]
    [ValidateNotNullOrEmpty()]
    [string] $Proposal,

    [Parameter(Mandatory = $true, Position = 1)]
    [ValidateNotNullOrEmpty()]
    [string] $Evidence,

    [Parameter(Position = 2, ValueFromRemainingArguments = $true)]
    [string[]] $Rest
)

try {
    $Application = Get-Command -Name "session-reviewer" -CommandType Application -ErrorAction Stop | Select-Object -First 1
    if ($null -eq $Application -or [string]::IsNullOrWhiteSpace($Application.Path)) {
        throw "application path is empty"
    }
}
catch {
    [Console]::Error.WriteLine("apply-proposal.ps1: session-reviewer application executable not found")
    exit 127
}

try {
    & $Application.Path apply --proposal $Proposal --evidence $Evidence @Rest
    $ExitCode = $LASTEXITCODE
    if ($null -eq $ExitCode) {
        throw "application did not return an exit code"
    }
}
catch {
    [Console]::Error.WriteLine("apply-proposal.ps1: session-reviewer application executable failed to start")
    exit 126
}
exit ([int] $ExitCode)
