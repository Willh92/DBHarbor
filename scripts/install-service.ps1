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

  # Least-privilege install under NT AUTHORITY\LocalService + custom Windows permission group
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Install `
    -ServiceAccount "NT AUTHORITY\LocalService" -PermissionGroup "Administrators" -Port 8787

  # Manage service lifecycle
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Status
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Restart
  powershell -ExecutionPolicy Bypass -File .\scripts\install-service.ps1 -Action Uninstall
#>

param(
  [ValidateSet("Install", "Uninstall", "Start", "Stop", "Restart", "Status")]
  [string]$Action = "Install",
  [string]$BinaryPath = "",
  [string]$HostAddr = "0.0.0.0",
  [int]$Port = 8787,
  [string]$DataDir = "",
  [string]$ServiceAccount = "SYSTEM",
  [string]$ServicePassword = "",
  [string]$PermissionGroup = "Administrators"
)

$ErrorActionPreference = "Stop"
$TaskName = "DBHarborServer"

$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path

if (-not $DataDir) {
  $ProgramDataRoot = if ($env:ProgramData) { $env:ProgramData } else { "C:\ProgramData" }
  $DataDir = Join-Path $ProgramDataRoot "DBHarbor\data"
}

function Resolve-DBHarborBinary {
  if ($BinaryPath -and (Test-Path $BinaryPath)) {
    return (Resolve-Path $BinaryPath).Path
  }
  $Candidates = @(
    (Join-Path $ScriptDir "dbharbor-windows-$Arch.exe"),
    (Join-Path $ScriptDir "..\dist\dbharbor-windows-$Arch.exe"),
    (Join-Path $ScriptDir "dbharbor.exe")
  )
  foreach ($c in $Candidates) {
    if (Test-Path $c) {
      return (Resolve-Path $c).Path
    }
  }
  throw "Could not find dbharbor-windows-$Arch.exe. Run 'npm run build' first or specify -BinaryPath."
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

switch ($Action) {
  "Install" {
    $SrcBin = Resolve-DBHarborBinary
    $InstallDir = Join-Path $env:ProgramFiles "DBHarbor"
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null

    Set-DataDirPermissions -TargetDir $DataDir -Account $ServiceAccount -Group $PermissionGroup

    $DestBin = Join-Path $InstallDir "dbharbor.exe"
    if ($SrcBin -ne $DestBin) {
      Copy-Item -Force $SrcBin $DestBin
    }

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
  "Uninstall" {
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "[DBHarbor] Uninstalled auto-start task '$TaskName' (data preserved at $DataDir)" -ForegroundColor Green
  }
  "Start" {
    Start-ScheduledTask -TaskName $TaskName
    Write-Host "[DBHarbor] Started '$TaskName'" -ForegroundColor Green
  }
  "Stop" {
    Stop-ScheduledTask -TaskName $TaskName
    Write-Host "[DBHarbor] Stopped '$TaskName'" -ForegroundColor Yellow
  }
  "Restart" {
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
    Start-ScheduledTask -TaskName $TaskName
    Write-Host "[DBHarbor] Restarted '$TaskName'" -ForegroundColor Green
  }
  "Status" {
    Get-ScheduledTask -TaskName $TaskName | Select-Object TaskName, State, @{N="UserId";E={$_.Principal.UserId}} | Format-Table -AutoSize
  }
}
