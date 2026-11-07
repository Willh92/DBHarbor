<#
.SYNOPSIS
  DBHarbor Windows Auto-Start Service Task Installer (with Service Account Role & ACL Group Hardening)
  Repository: https://github.com/Willh92/DBHarbor.git
  License: Apache-2.0

.DESCRIPTION
  Installs dbharbor.exe under %ProgramFiles%\DBHarbor, stores runtime metadata & encryption keys
  under %ProgramData%\DBHarbor\data (never in the startup directory), configures Windows ACLs
  for the chosen service role & permission group, and registers a boot-time Scheduled Task service.

.EXAMPLE
  # Default install under SYSTEM with data at C:\ProgramData\DBHarbor\data
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Install -Port 8787

  # Least-privilege install under NT AUTHORITY\LocalService + custom initial admin account (>= 8 char password)
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Install `
    -ServiceAccount "NT AUTHORITY\LocalService" -PermissionGroup "Administrators" `
    -AdminUser "ops_admin" -AdminPass "ChangeMe!2026" -Port 8787

  # Upgrade (or Update) binary online from GitHub Releases with SHA-256 verification and restart service
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Upgrade

  # Manage service lifecycle
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Status
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Restart
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Uninstall
#>

param(
  [ValidateSet("Install", "Upgrade", "Update", "Uninstall", "Start", "Stop", "Restart", "Status")]
  [string]$Action = "Install",
  [string]$BinaryPath = "",
  [string]$InstallDir = "",
  [switch]$Download,
  [string]$Version = "",
  [ValidateSet("release", "preview")]
  [string]$Channel = "release",
  [string]$Mirror = "",
  [string]$Repo = "Willh92/DBHarbor",
  [switch]$NoVerify,
  [string]$HostAddr = "0.0.0.0",
  [int]$Port = 8787,
  [string]$DataDir = "",
  [string]$AdminUser = $env:DBHARBOR_ADMIN_USER,
  [string]$AdminPass = $env:DBHARBOR_ADMIN_PASS,
  [string]$ServiceAccount = "SYSTEM",
  [string]$ServicePassword = "",
  [string]$PermissionGroup = "Administrators"
)

if ($Action -eq "Update") { $Action = "Upgrade" }

$ErrorActionPreference = "Stop"
$TaskName = "DBHarborServer"
$Script:TempDownloadDir = ""

if ($AdminUser -and (-not $AdminPass)) {
  throw "Invalid parameters: -AdminPass (at least 8 characters) is required when -AdminUser is specified."
}
if ($AdminUser -and ($AdminUser -notmatch '^[a-zA-Z0-9._@-]{2,64}$')) {
  throw "Invalid -AdminUser '$AdminUser': must be 2-64 characters using letters, numbers, '.', '_', '-', or '@'."
}
if ($AdminPass -and ($AdminPass.Length -lt 8)) {
  throw "Invalid -AdminPass: initial admin password must be at least 8 characters."
}

$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$ScriptDir = if ($MyInvocation.MyCommand.Path) { Split-Path -Parent $MyInvocation.MyCommand.Path } else { (Get-Location).Path }

if ($InstallDir) {
  $InstallDir = [System.IO.Path]::GetFullPath($InstallDir)
  if (-not $DataDir) {
    $DataDir = Join-Path $InstallDir "data"
  }
} else {
  $InstallDir = Join-Path $env:ProgramFiles "DBHarbor"
  if (-not $DataDir) {
    $ProgramDataRoot = if ($env:ProgramData) { $env:ProgramData } else { "C:\ProgramData" }
    $DataDir = Join-Path $ProgramDataRoot "DBHarbor\data"
  }
}
$DataDir = [System.IO.Path]::GetFullPath($DataDir)

function Get-ReleaseBaseUrl {
  param([string]$Ref)
  $rawBase = if ($Ref -eq "latest") {
    "https://github.com/$Repo/releases/latest/download"
  } else {
    "https://github.com/$Repo/releases/download/$Ref"
  }
  if ($Mirror) {
    return ($Mirror.TrimEnd('/') + "/" + $rawBase)
  }
  return $rawBase
}

function Download-ReleaseBinary {
  param([string]$AssetName)
  $pinned = $false
  $primary = "latest"
  if ($Version) {
    if ($Version -eq "latest") {
      $primary = "latest"
    } elseif ($Version -eq "preview") {
      $primary = "preview"
      $pinned = $true
    } else {
      $cleanVer = $Version.TrimStart('v', 'V')
      $primary = "v$cleanVer"
      $pinned = $true
    }
  } elseif ($Channel -eq "preview") {
    $primary = "preview"
    $pinned = $true
  }

  if ($Script:TempDownloadDir -and (Test-Path $Script:TempDownloadDir)) {
    Remove-Item -Recurse -Force $Script:TempDownloadDir -ErrorAction SilentlyContinue
  }
  $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("dbharbor-dl-" + [Guid]::NewGuid().ToString("N"))
  New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
  $Script:TempDownloadDir = $tmpDir
  $dlBin = Join-Path $tmpDir $AssetName
  $dlSum = Join-Path $tmpDir "SHA256SUMS.txt"

  $baseUrl = Get-ReleaseBaseUrl -Ref $primary
  Write-Host "[DBHarbor] Downloading $AssetName from GitHub Releases ($Repo @ $primary)..." -ForegroundColor Cyan
  try {
    & curl.exe -fL --retry 3 --connect-timeout 15 -o $dlBin "$baseUrl/$AssetName"
    if ($LASTEXITCODE -ne 0) { throw "HTTP download failed" }
  } catch {
    if (($primary -eq "latest") -and (-not $pinned)) {
      Write-Host "[DBHarbor] No stable 'latest' release found; falling back to rolling 'preview' release..." -ForegroundColor Yellow
      $primary = "preview"
      $baseUrl = Get-ReleaseBaseUrl -Ref "preview"
      & curl.exe -fL --retry 3 --connect-timeout 15 -o $dlBin "$baseUrl/$AssetName"
      if ($LASTEXITCODE -ne 0) {
        throw "Failed to download $AssetName from $baseUrl/$AssetName"
      }
    } else {
      throw "Failed to download $AssetName from $baseUrl/$AssetName"
    }
  }

  if ((-not (Test-Path $dlBin)) -or ((Get-Item $dlBin).Length -eq 0)) {
    throw "Downloaded binary is missing or empty: $dlBin"
  }

  if (-not $NoVerify) {
    & curl.exe -fsL --retry 3 --connect-timeout 15 -o $dlSum "$baseUrl/SHA256SUMS.txt"
    if (($LASTEXITCODE -ne 0) -or (-not (Test-Path $dlSum)) -or ((Get-Item $dlSum).Length -eq 0)) {
      throw "Failed to download SHA256SUMS.txt from $baseUrl/SHA256SUMS.txt (refusing to install unverified binary; pass -NoVerify only when using a custom mirror without checksums)."
    }
    $expected = $null
    foreach ($line in (Get-Content $dlSum)) {
      if ($line -match "^([0-9a-fA-F]{64})\s+\*?$([Regex]::Escape($AssetName))\s*$") {
        $expected = $Matches[1].ToLowerInvariant()
        break
      }
    }
    if (-not $expected) {
      throw "Entry for '$AssetName' not found in SHA256SUMS.txt from $baseUrl/SHA256SUMS.txt (refusing to install unverified binary; pass -NoVerify only when using a custom mirror without checksums)."
    }
    $actual = (Get-FileHash -Path $dlBin -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
      throw "SHA-256 mismatch for $AssetName! Expected $expected, got $actual"
    }
    Write-Host "[DBHarbor] Verified SHA-256 checksum ($actual)" -ForegroundColor Green
  } else {
    Write-Host "[DBHarbor] WARNING: Skipping SHA-256 checksum verification (-NoVerify specified)." -ForegroundColor Yellow
  }

  $prevEap = $ErrorActionPreference
  $verOut = ""
  $verExit = 0
  try {
    $ErrorActionPreference = "Continue"
    $verOut = (& $dlBin -version 2>&1 | Out-String).Trim()
    $verExit = $LASTEXITCODE
  } catch {
    $verOut = $_.Exception.Message
    $verExit = 1
  } finally {
    $ErrorActionPreference = $prevEap
  }
  if ($verExit -ne 0) {
    throw "Downloaded binary failed smoke check (-version): $verOut"
  }

  return $dlBin
}

function Resolve-DBHarborBinary {
  if ($BinaryPath) {
    if (-not (Test-Path $BinaryPath)) {
      throw "Specified binary not found: $BinaryPath"
    }
    return (Resolve-Path $BinaryPath).Path
  }
  $AssetName = "dbharbor-windows-$Arch.exe"
  if ($Download -or $Version -or ($Action -eq "Upgrade")) {
    return (Download-ReleaseBinary -AssetName $AssetName)
  }
  $Candidates = @(
    (Join-Path $ScriptDir $AssetName),
    (Join-Path $ScriptDir "..\dist\$AssetName"),
    (Join-Path $ScriptDir "dbharbor.exe")
  )
  foreach ($c in $Candidates) {
    if (Test-Path $c) {
      Write-Host "[DBHarbor] Using local binary: $c" -ForegroundColor Cyan
      return (Resolve-Path $c).Path
    }
  }
  Write-Host "[DBHarbor] No local binary ($AssetName) found; auto-downloading from GitHub Releases..." -ForegroundColor Cyan
  return (Download-ReleaseBinary -AssetName $AssetName)
}

function Get-ProcessesForExecutable {
  param([string]$FullExePath)
  if (-not $FullExePath) { return @() }
  $leafNoExt = [System.IO.Path]::GetFileNameWithoutExtension($FullExePath)
  if (-not $leafNoExt) { return @() }
  $matched = @()
  foreach ($proc in @(Get-Process -Name $leafNoExt -ErrorAction SilentlyContinue)) {
    $pPath = $null
    try { $pPath = $proc.Path } catch {}
    if (-not $pPath) {
      try {
        $cimProc = Get-CimInstance Win32_Process -Filter "ProcessId = $($proc.Id)" -ErrorAction SilentlyContinue
        if ($cimProc) { $pPath = $cimProc.ExecutablePath }
      } catch {}
    }
    if ($pPath) {
      try {
        if ([string]::Equals([System.IO.Path]::GetFullPath($pPath), $FullExePath, [System.StringComparison]::OrdinalIgnoreCase)) {
          $matched += $proc
        }
      } catch {}
    }
  }
  return $matched
}

function Stop-DBHarborServiceAndWait {
  param([string]$TargetExe = "")
  $fullTarget = if ($TargetExe) { [System.IO.Path]::GetFullPath($TargetExe) } else { "" }
  Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue

  for ($i = 0; $i -lt 16; $i++) {
    $taskObj = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    $taskRunning = ($null -ne $taskObj) -and ($taskObj.State -eq "Running")
    $runningProcs = @(Get-ProcessesForExecutable -FullExePath $fullTarget)
    if ((-not $taskRunning) -and ($runningProcs.Count -eq 0)) {
      return
    }
    Start-Sleep -Milliseconds 500
  }

  if ($fullTarget) {
    $lingering = @(Get-ProcessesForExecutable -FullExePath $fullTarget)
    if ($lingering.Count -gt 0) {
      foreach ($p in $lingering) {
        Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
      }
      Start-Sleep -Milliseconds 500
    }
  }
}

function Install-BinarySafe {
  param(
    [string]$SourceExe,
    [string]$TargetExe
  )
  $fullSrc = [System.IO.Path]::GetFullPath($SourceExe)
  $fullDst = [System.IO.Path]::GetFullPath($TargetExe)
  if ([string]::Equals($fullSrc, $fullDst, [System.StringComparison]::OrdinalIgnoreCase)) {
    return
  }

  $parentDir = Split-Path -Parent $fullDst
  if ($parentDir) {
    New-Item -ItemType Directory -Force -Path $parentDir | Out-Null
  }

  $oldExe = "$fullDst.old"
  $newExe = "$fullDst.new"
  Remove-Item -Force $oldExe -ErrorAction SilentlyContinue
  Remove-Item -Force $newExe -ErrorAction SilentlyContinue

  Copy-Item -Force $fullSrc $newExe
  try {
    Move-Item -Force $newExe $fullDst
  } catch {
    if (Test-Path $fullDst) {
      Move-Item -Force $fullDst $oldExe
    }
    Move-Item -Force $newExe $fullDst
    Remove-Item -Force $oldExe -ErrorAction SilentlyContinue
  }
}

function Get-RegisteredTaskExecutable {
  param($TaskObj, [string]$FallbackExe)
  if ($TaskObj -and $TaskObj.Actions -and ($TaskObj.Actions.Count -gt 0) -and $TaskObj.Actions[0].Execute) {
    $trimmed = $TaskObj.Actions[0].Execute.Trim('"')
    if ($trimmed) {
      return [System.IO.Path]::GetFullPath($trimmed)
    }
  }
  return [System.IO.Path]::GetFullPath($FallbackExe)
}

function Set-DataDirPermissions {
  param(
    [string]$TargetDir,
    [string]$Account,
    [string]$Group
  )
  # Restrict C:\ProgramData\DBHarbor\data to SYSTEM, Administrators, ServiceAccount, and PermissionGroup
  & icacls $TargetDir /inheritance:r | Out-Null
  & icacls $TargetDir /grant:r "SYSTEM:(OI)(CI)F" "Administrators:(OI)(CI)F" | Out-Null
  if ($Account -and ($Account -ne "SYSTEM") -and ($Account -ne "NT AUTHORITY\SYSTEM")) {
    & icacls $TargetDir /grant:r "${Account}:(OI)(CI)M" | Out-Null
  }
  if ($Group -and ($Group -ne "Administrators") -and ($Group -ne "BUILTIN\Administrators")) {
    & icacls $TargetDir /grant:r "${Group}:(OI)(CI)RX" | Out-Null
  }
}

function Initialize-AdminStore {
  param(
    [string]$Executable,
    [string]$TargetDataDir
  )
  if ((-not $AdminUser) -and (-not $AdminPass)) {
    return
  }
  if (-not $AdminPass) {
    throw "Invalid parameters: -AdminPass (at least 8 characters) is required when initializing an administrator account."
  }
  $DbFile = Join-Path $TargetDataDir "dbharbor.db"
  if ((Test-Path $DbFile) -and ((Get-Item $DbFile).Length -gt 0)) {
    Write-Host "[DBHarbor] Existing database detected at $DbFile; -AdminUser / -AdminPass only applies when initializing a fresh data directory." -ForegroundColor Yellow
    return
  }

  $EffUser = if ($AdminUser) { $AdminUser } else { "admin" }
  $EffPass = $AdminPass
  $PrevUser = $env:DBHARBOR_ADMIN_USER
  $PrevPass = $env:DBHARBOR_ADMIN_PASS
  try {
    $env:DBHARBOR_ADMIN_USER = $EffUser
    $env:DBHARBOR_ADMIN_PASS = $EffPass

    $prevEap = $ErrorActionPreference
    $HelpOut = ""
    try {
      $ErrorActionPreference = "Continue"
      $HelpOut = (& $Executable -h 2>&1 | Out-String)
    } catch {
      $HelpOut = ""
    } finally {
      $ErrorActionPreference = $prevEap
    }

    if ($HelpOut -match "init-only") {
      & $Executable -data-dir $TargetDataDir -init-only | Out-Null
      if ($LASTEXITCODE -ne 0) {
        throw "Failed to initialize admin store with -init-only"
      }
    } else {
      $Proc = Start-Process -FilePath $Executable -ArgumentList @("-host", "127.0.0.1", "-port", "0", "-data-dir", "`"$TargetDataDir`"") -WindowStyle Hidden -PassThru
      try {
        for ($i = 0; $i -lt 20; $i++) {
          if ((Test-Path $DbFile) -and ((Get-Item $DbFile).Length -gt 0)) {
            break
          }
          Start-Sleep -Milliseconds 500
        }
      } finally {
        if (-not $Proc.HasExited) {
          Stop-Process -Id $Proc.Id -Force -ErrorAction SilentlyContinue
        }
      }
    }
    Write-Host "[DBHarbor] Initialized administrator account ($EffUser) in $TargetDataDir" -ForegroundColor Green
  } finally {
    $env:DBHARBOR_ADMIN_USER = $PrevUser
    $env:DBHARBOR_ADMIN_PASS = $PrevPass
  }
}

try {
  switch ($Action) {
    "Install" {
      $SrcBin = Resolve-DBHarborBinary
      New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
      New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

      $DestBin = [System.IO.Path]::GetFullPath((Join-Path $InstallDir "dbharbor.exe"))
      $ExistingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
      if ($ExistingTask -or (Test-Path $DestBin)) {
        if ($ExistingTask) {
          $PrevExe = Get-RegisteredTaskExecutable -TaskObj $ExistingTask -FallbackExe $DestBin
          if (-not [string]::Equals($PrevExe, $DestBin, [System.StringComparison]::OrdinalIgnoreCase)) {
            Stop-DBHarborServiceAndWait -TargetExe $PrevExe
          }
        }
        Stop-DBHarborServiceAndWait -TargetExe $DestBin
      }

      Install-BinarySafe -SourceExe $SrcBin -TargetExe $DestBin

      Initialize-AdminStore -Executable $DestBin -TargetDataDir $DataDir

      if ($ServiceAccount -and ($ServiceAccount -ne "SYSTEM") -and ($ServiceAccount -ne "NT AUTHORITY\SYSTEM")) {
        & icacls $InstallDir /grant "${ServiceAccount}:(OI)(CI)RX" | Out-Null
      }
      Set-DataDirPermissions -TargetDir $DataDir -Account $ServiceAccount -Group $PermissionGroup

      $ArgsStr = "-host $HostAddr -port $Port -data-dir `"$DataDir`""
      $TaskAction = New-ScheduledTaskAction -Execute $DestBin -Argument $ArgsStr -WorkingDirectory $InstallDir
      $TaskTrigger = New-ScheduledTaskTrigger -AtStartup
      $TaskSettings = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 3 `
        -RestartInterval (New-TimeSpan -Minutes 1)

      $BuiltInAccounts = @("SYSTEM", "NT AUTHORITY\SYSTEM", "NT AUTHORITY\LocalService", "NT AUTHORITY\NetworkService", "LocalService", "NetworkService")
      if ($BuiltInAccounts -contains $ServiceAccount) {
        $Principal = New-ScheduledTaskPrincipal -UserId $ServiceAccount -LogonType ServiceAccount -RunLevel Highest
        Register-ScheduledTask -TaskName $TaskName -Action $TaskAction -Trigger $TaskTrigger -Settings $TaskSettings -Principal $Principal -Force | Out-Null
      } elseif ($ServicePassword) {
        Register-ScheduledTask -TaskName $TaskName -Action $TaskAction -Trigger $TaskTrigger -Settings $TaskSettings -User $ServiceAccount -Password $ServicePassword -RunLevel Highest -Force | Out-Null
      } else {
        $Principal = New-ScheduledTaskPrincipal -UserId $ServiceAccount -LogonType S4U -RunLevel Highest
        Register-ScheduledTask -TaskName $TaskName -Action $TaskAction -Trigger $TaskTrigger -Settings $TaskSettings -Principal $Principal -Force | Out-Null
      }

      Start-ScheduledTask -TaskName $TaskName
      Write-Host "[DBHarbor] Installed & started Windows service task '$TaskName'" -ForegroundColor Green
      Write-Host "  Binary   : $DestBin"
      Write-Host "  Data Dir : $DataDir"
      Write-Host "  Role/ACL : Account=$ServiceAccount, Group=$PermissionGroup"
      Write-Host "  Listen   : http://${HostAddr}:${Port}"
    }
    "Upgrade" {
      $ExistingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
      $DestBin = [System.IO.Path]::GetFullPath((Join-Path $InstallDir "dbharbor.exe"))
      if ($ExistingTask -and (-not $PSBoundParameters.ContainsKey("InstallDir"))) {
        $DestBin = Get-RegisteredTaskExecutable -TaskObj $ExistingTask -FallbackExe $DestBin
      }
      $SrcBin = Resolve-DBHarborBinary
      if ($ExistingTask -or (Test-Path $DestBin)) {
        Stop-DBHarborServiceAndWait -TargetExe $DestBin
      }
      Install-BinarySafe -SourceExe $SrcBin -TargetExe $DestBin
      if ($ExistingTask) {
        Start-ScheduledTask -TaskName $TaskName
        Write-Host "[DBHarbor] Upgraded & restarted '$TaskName' ($DestBin)" -ForegroundColor Green
      } else {
        Write-Host "[DBHarbor] Upgraded binary at $DestBin" -ForegroundColor Green
      }
      & $DestBin -version
    }
    "Uninstall" {
      $ExistingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
      $TargetExe = Get-RegisteredTaskExecutable -TaskObj $ExistingTask -FallbackExe (Join-Path $InstallDir "dbharbor.exe")
      Stop-DBHarborServiceAndWait -TargetExe $TargetExe
      Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
      Write-Host "[DBHarbor] Uninstalled auto-start task '$TaskName' (data preserved at $DataDir)" -ForegroundColor Green
    }
    "Start" {
      Start-ScheduledTask -TaskName $TaskName
      Write-Host "[DBHarbor] Started '$TaskName'" -ForegroundColor Green
    }
    "Stop" {
      $ExistingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
      $TargetExe = Get-RegisteredTaskExecutable -TaskObj $ExistingTask -FallbackExe (Join-Path $InstallDir "dbharbor.exe")
      Stop-DBHarborServiceAndWait -TargetExe $TargetExe
      Write-Host "[DBHarbor] Stopped '$TaskName'" -ForegroundColor Yellow
    }
    "Restart" {
      $ExistingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
      $TargetExe = Get-RegisteredTaskExecutable -TaskObj $ExistingTask -FallbackExe (Join-Path $InstallDir "dbharbor.exe")
      Stop-DBHarborServiceAndWait -TargetExe $TargetExe
      Start-ScheduledTask -TaskName $TaskName
      Write-Host "[DBHarbor] Restarted '$TaskName'" -ForegroundColor Green
    }
    "Status" {
      Get-ScheduledTask -TaskName $TaskName | Select-Object TaskName, State, @{N="UserId";E={$_.Principal.UserId}} | Format-Table -AutoSize
    }
  }
} finally {
  if ($Script:TempDownloadDir -and (Test-Path $Script:TempDownloadDir)) {
    Remove-Item -Recurse -Force $Script:TempDownloadDir -ErrorAction SilentlyContinue
  }
}
