<#
.SYNOPSIS
  Back up the interior database and uploaded media.

.DESCRIPTION
  Writes a compressed pg_dump (custom format, restorable with pg_restore)
  and a zip of the media storage folder, side by side and with the same
  timestamp, so a restore brings both back to the same moment.

  The database URL comes from -DatabaseUrl, else $env:DATABASE_URL, else
  DATABASE_URL in .env. Nothing is printed that contains the password.

.EXAMPLE
  .\scripts\backup.ps1
  .\scripts\backup.ps1 -OutDir D:\backups\interior -PgBin "D:\postgres-18\bin"
#>
param(
  [string]$DatabaseUrl = $env:DATABASE_URL,
  [string]$OutDir = "backups",
  [string]$StorageDir = $(if ($env:STORAGE_DIR) { $env:STORAGE_DIR } else { "storage" }),
  [string]$PgBin = "D:\postgres-18\bin"
)
$ErrorActionPreference = "Stop"

if (-not $DatabaseUrl -and (Test-Path ".env")) {
  $line = Get-Content ".env" | Where-Object { $_ -match '^\s*DATABASE_URL\s*=' } | Select-Object -First 1
  if ($line) { $DatabaseUrl = ($line -split '=', 2)[1].Trim() }
}
if (-not $DatabaseUrl) { throw "DATABASE_URL is not set (pass -DatabaseUrl or set it in .env)" }

$stamp = Get-Date -Format "yyyyMMdd_HHmmss"
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$dump = Join-Path $OutDir "interior_db_$stamp.dump"
$media = Join-Path $OutDir "interior_storage_$stamp.zip"

# -Fc: custom format (compressed, selective restore possible).
& (Join-Path $PgBin "pg_dump.exe") --format=custom --no-owner --file=$dump $DatabaseUrl
if ($LASTEXITCODE -ne 0) { throw "pg_dump failed (exit $LASTEXITCODE)" }
Write-Host "database -> $dump ($([math]::Round((Get-Item $dump).Length / 1KB)) KB)"

if (Test-Path $StorageDir) {
  Compress-Archive -Path (Join-Path $StorageDir "*") -DestinationPath $media -Force
  Write-Host "media    -> $media"
} else {
  Write-Host "media    -> skipped ($StorageDir does not exist)"
}
