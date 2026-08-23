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

& session-reviewer apply --proposal $Proposal --evidence $Evidence @Rest
exit $LASTEXITCODE
