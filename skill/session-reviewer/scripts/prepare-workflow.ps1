param(
    [Parameter(Mandatory = $true, Position = 0)]
    [ValidateSet("review", "checkpoint")]
    [string] $Mode,

    [Parameter(Mandatory = $true, Position = 1)]
    [ValidateNotNullOrEmpty()]
    [string] $Output,

    [Parameter(Position = 2, ValueFromRemainingArguments = $true)]
    [string[]] $Rest
)

& session-reviewer prepare $Mode --output $Output @Rest
exit $LASTEXITCODE
