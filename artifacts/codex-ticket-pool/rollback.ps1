[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $ApiUrl,
    [Parameter(Mandatory = $true)]
    [string] $PreviousProxyURLs,
    [string] $Token = $env:SUB2API_ADMIN_TOKEN,
    [switch] $RestoreBusinessLinkage,
    [switch] $CheckOnly
)

$ErrorActionPreference = 'Stop'

function Get-SettingsEndpoint([string] $raw) {
    $value = $raw.TrimEnd('/')
    if ($value -match '/admin/settings$') { return $value }
    if ($value -match '/api/v1$') { return "$value/admin/settings" }
    return "$value/api/v1/admin/settings"
}

$endpoint = Get-SettingsEndpoint $ApiUrl
$headers = @{ Accept = 'application/json' }
if ($Token) { $headers.Authorization = "Bearer $Token" }
$payload = @{ openai_codex_ticket_harvest_proxy_url = $PreviousProxyURLs }
if ($RestoreBusinessLinkage) {
    $payload.openai_codex_ticket_sync_business_proxy = $true
}

if (-not $CheckOnly) {
    Invoke-RestMethod -Method Put -Uri $endpoint -Headers $headers -ContentType 'application/json' -Body ($payload | ConvertTo-Json -Compress) | Out-Null
}

$observed = Invoke-RestMethod -Method Get -Uri $endpoint -Headers $headers
$data = if ($observed.data) { $observed.data } else { $observed }
$observedLines = @(([string]$data.openai_codex_ticket_harvest_proxy_url) -split "`r?`n" | Where-Object { $_.Trim() })
$expectedLines = @($PreviousProxyURLs -split "`r?`n" | Where-Object { $_.Trim() })
if ($observedLines.Count -ne $expectedLines.Count) {
    throw 'rollback verification failed: pool entry count differs'
}
if (-not $RestoreBusinessLinkage -and $expectedLines.Count -gt 1 -and $data.openai_codex_ticket_sync_business_proxy -ne $false) {
    throw 'rollback verification failed: multi-entry linkage is not disabled'
}
if ($RestoreBusinessLinkage -and $expectedLines.Count -le 1 -and $data.openai_codex_ticket_sync_business_proxy -ne $true) {
    throw 'rollback verification failed: single-entry linkage is not enabled'
}
Write-Output ("rollback-verified entries={0} check_only={1}" -f $expectedLines.Count, $CheckOnly.IsPresent)
