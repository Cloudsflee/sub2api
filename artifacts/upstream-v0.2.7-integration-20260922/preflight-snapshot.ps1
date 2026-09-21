[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string] $ApiUrl,
    [Parameter(Mandatory = $true)]
    [string] $OutputDir,
    [Parameter(Mandatory = $true)]
    [long[]] $AccountIds,
    [string] $Token = $env:SUB2API_ADMIN_TOKEN,
    [string] $DatabaseUrl = $env:DATABASE_URL,
    [string] $PgDump = 'pg_dump',
    [string] $Psql = 'psql',
    [switch] $SkipDatabase,
    [switch] $SkipDocker
)

$ErrorActionPreference = 'Stop'
$uri = [Uri]$ApiUrl
if ($uri.Host -eq 'api.sub2api.com') {
    throw 'the external third-party host is not a deployment or snapshot target'
}
if ([string]::IsNullOrWhiteSpace($Token)) { throw 'an admin API token is required' }
if ($AccountIds.Count -lt 1) { throw 'at least one target account ID is required' }

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$root = $ApiUrl.TrimEnd('/')
if ($root -notmatch '/api/v1$') { $root = "$root/api/v1" }
$headers = @{ Accept = 'application/json'; 'x-api-key' = $Token }

function Get-Data($value) {
    if ($null -ne $value.data) { return $value.data }
    return $value
}

$settingsResponse = Invoke-RestMethod -Method Get -Uri "$root/admin/settings" -Headers $headers
$settings = Get-Data $settingsResponse
[ordered]@{
    captured_at = (Get-Date).ToUniversalTime().ToString('o')
    openai_codex_ticket_enabled = [bool]$settings.openai_codex_ticket_enabled
    openai_codex_ticket_sync_business_proxy = [bool]$settings.openai_codex_ticket_sync_business_proxy
    openai_codex_ticket_account_ids = @($settings.openai_codex_ticket_account_ids)
    openai_codex_ticket_account_models = $settings.openai_codex_ticket_account_models
} | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $OutputDir 'settings-safe.json') -Encoding utf8

$safeAccounts = @()
foreach ($id in $AccountIds) {
    $accountResponse = Invoke-RestMethod -Method Get -Uri "$root/admin/accounts/$id" -Headers $headers
    $account = Get-Data $accountResponse
    $extra = @{}
    if ($null -ne $account.extra) {
        foreach ($key in @($account.extra.PSObject.Properties.Name)) {
            if ($key -eq 'codex_fingerprint_mode' -or $key -eq 'codex_fingerprint_seed' -or $key -like 'codex_turn_ticket:*') {
                if ($key -eq 'codex_fingerprint_seed') {
                    $extra[$key] = '<redacted-seed>'
                } elseif ($key -like 'codex_turn_ticket:*') {
                    $ticket = $account.extra.$key
                    $extra[$key] = [ordered]@{
                        present = $true
                        length = if ($null -ne $ticket.length) { [int]$ticket.length } else { $null }
                        expires_at = if ($null -ne $ticket.expires_at) { [string]$ticket.expires_at } else { $null }
                    }
                } else {
                    $extra[$key] = $account.extra.$key
                }
            }
        }
    }
    $proxyID = $account.proxy_id
    if ($null -eq $proxyID -and $null -ne $account.proxy) { $proxyID = $account.proxy.id }
    $safeAccounts += [ordered]@{
        id = [int64]$account.id
        platform = [string]$account.platform
        type = [string]$account.type
        status = [string]$account.status
        proxy_id = if ($null -eq $proxyID) { $null } else { [int64]$proxyID }
        extra_summary = $extra
    }
}
$safeAccounts | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $OutputDir 'accounts-safe.json') -Encoding utf8

if (-not $SkipDatabase) {
    if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) { throw 'DatabaseUrl is required unless -SkipDatabase is set' }
    & $Psql $DatabaseUrl -Atc 'SELECT filename, checksum FROM schema_migrations ORDER BY filename' | Set-Content -LiteralPath (Join-Path $OutputDir 'schema_migrations.tsv') -Encoding utf8
    & $PgDump $DatabaseUrl --format=custom --file (Join-Path $OutputDir 'postgres-preflight.dump')
    if ($LASTEXITCODE -ne 0) { throw "pg_dump failed with exit code $LASTEXITCODE" }
}

if (-not $SkipDocker -and (Get-Command docker -ErrorAction SilentlyContinue)) {
    docker ps --format '{{.Names}}\t{{.Image}}\t{{.Status}}' | Set-Content -LiteralPath (Join-Path $OutputDir 'docker-state.tsv') -Encoding utf8
}

Get-ChildItem -LiteralPath $OutputDir -File | Get-FileHash -Algorithm SHA256 |
    Select-Object Algorithm, Hash, @{Name='Path'; Expression={$_.Path}} |
    ConvertTo-Csv -NoTypeInformation | Set-Content -LiteralPath (Join-Path $OutputDir 'SHA256SUMS.csv') -Encoding utf8
Write-Output ("preflight-snapshot-created path={0}" -f (Resolve-Path $OutputDir))
