[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $ApiUrl,
    [Parameter(Mandatory = $true)]
    [long[]] $AccountIds,
    [string] $Token = $env:SUB2API_ADMIN_TOKEN,
    [int64] $ExpectedProxyId = 1,
    [ValidateSet('device', 'off')]
    [string] $Mode = 'device',
    [string] $SnapshotPath,
    [switch] $CheckOnly
)

$ErrorActionPreference = 'Stop'

if ($AccountIds.Count -ne 2) {
    throw 'exactly two target account IDs are required'
}
if ([string]::IsNullOrWhiteSpace($Token)) {
    throw 'an admin API token is required via -Token or SUB2API_ADMIN_TOKEN'
}

function Get-ApiRoot([string] $raw) {
    $value = $raw.TrimEnd('/')
    if ($value -match '/api/v1$') { return $value }
    return "$value/api/v1"
}

function Get-EnvelopeData($value) {
    if ($null -ne $value.data) { return $value.data }
    return $value
}

$root = Get-ApiRoot $ApiUrl
$headers = @{ Accept = 'application/json'; 'x-api-key' = $Token }
$accounts = @()
foreach ($id in $AccountIds) {
    $response = Invoke-RestMethod -Method Get -Uri "$root/admin/accounts/$id" -Headers $headers
    $account = Get-EnvelopeData $response
    if ($null -eq $account) { throw "account $id was not returned" }
    if ([string]$account.platform -ne 'openai' -or [string]$account.type -ne 'oauth') {
        throw "account $id is not an OpenAI OAuth account"
    }
    if ($null -ne $account.parent_account_id) {
        throw "account $id is a shadow account"
    }
    $proxyId = $account.proxy_id
    if ($null -eq $proxyId -and $null -ne $account.proxy) { $proxyId = $account.proxy.id }
    if ([int64]$proxyId -ne $ExpectedProxyId) {
        throw "account $id proxy_id is $proxyId, expected $ExpectedProxyId"
    }
    $accounts += [ordered]@{
        id = [int64]$account.id
        proxy_id = [int64]$proxyId
        fingerprint_mode_before = [string]$account.extra.codex_fingerprint_mode
    }
}

if ($SnapshotPath) {
    $accounts | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $SnapshotPath -Encoding utf8
}

$payload = @{
    account_ids = @($AccountIds)
    extra = @{ codex_fingerprint_mode = $Mode }
} | ConvertTo-Json -Depth 5 -Compress

if (-not $CheckOnly) {
    Invoke-RestMethod -Method Post -Uri "$root/admin/accounts/bulk-update" -Headers $headers -ContentType 'application/json' -Body $payload | Out-Null
}

$verified = @()
foreach ($id in $AccountIds) {
    $response = Invoke-RestMethod -Method Get -Uri "$root/admin/accounts/$id" -Headers $headers
    $account = Get-EnvelopeData $response
    $proxyId = $account.proxy_id
    if ($null -eq $proxyId -and $null -ne $account.proxy) { $proxyId = $account.proxy.id }
    $mode = [string]$account.extra.codex_fingerprint_mode
    if ([int64]$proxyId -ne $ExpectedProxyId -or $mode -ne $Mode) {
        throw "verification failed for account $id (proxy_id=$proxyId mode=$mode)"
    }
    $verified += [ordered]@{ id = [int64]$id; proxy_id = [int64]$proxyId; fingerprint_mode = $mode }
}

$verified | ConvertTo-Json -Compress
Write-Output ("fingerprint-rollout-verified mode={0} check_only={1}" -f $Mode, $CheckOnly.IsPresent)
