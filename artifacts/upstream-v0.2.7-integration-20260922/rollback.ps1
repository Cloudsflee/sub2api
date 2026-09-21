[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $ApiUrl,
    [Parameter(Mandatory = $true)]
    [long[]] $AccountIds,
    [string] $Token = $env:SUB2API_ADMIN_TOKEN,
    [int64] $ExpectedProxyId = 1,
    [switch] $CheckOnly
)

$script = Join-Path $PSScriptRoot 'rollout-fingerprint.ps1'
& $script -ApiUrl $ApiUrl -AccountIds $AccountIds -Token $Token -ExpectedProxyId $ExpectedProxyId -Mode off -CheckOnly:$CheckOnly
if ($LASTEXITCODE -and $LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
Write-Output 'rollback-verified fingerprint_mode=off business_proxy_unchanged=true'
