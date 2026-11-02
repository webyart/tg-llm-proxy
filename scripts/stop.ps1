<#
the counterpart of scripts/run.ps1 -Background, which leaves the operator with nothing but Stop-Process.

run via: .\scripts\stop.ps1 -Id <pid>                # the graceful stop by a Ctrl+C event
         pwsh -NoProfile -File .\scripts\stop.ps1 -Id <pid>   # from a script (see «why a child»)
         pwsh -NoProfile -File .\scripts\stop.ps1 -Id <pid>   # from a script (see «why a child»)

What it does — deliver a graceful stop on every supported host. On Windows a hidden console receives
Ctrl+C through AttachConsole/GenerateConsoleCtrlEvent; on macOS/Linux Stop-Process sends SIGTERM. Both reach
`stopSignals` of main.go, so the process runs pollerWait, closes the store and checkpoints the WAL.

Why a child process: the process that attaches to the bot's console receives the Ctrl+C event it
generated and usually leaves with it. Run it as its own process
(`pwsh -NoProfile -File .\scripts\stop.ps1 -Id N`) so that its exit does not take the caller down.

Exit: 0 the event was posted and this process was able to leave on its own two feet.
      -1073741510 = 0xC000013A (STATUS_CONTROL_C_EXIT) is the EXPECTED outcome in practice: the
        process that attaches to the bot's console is itself attached to it, so it receives the very
        Ctrl+C it posted and dies from it. Measured 2026-10-31. The caller therefore verifies the stop
        by the target being gone, never by this exit code.
      1 no such process
Verify: do NOT use Wait-Process for the check — it throws «Cannot find a process with the process
        identifier» when the process is already gone, which reads exactly like a timeout. Poll instead:
        `while (Get-Process -Id N -ErrorAction SilentlyContinue) { Start-Sleep -Milliseconds 200 }`.
#>

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][int]$Id,
    [switch]$Force,
    [string]$ReportFile
)

$ErrorActionPreference = 'Stop'

function Say([string]$message) {
    Write-Output "stop: $message"
    if ($ReportFile) { Add-Content -LiteralPath $ReportFile -Value "stop: $message" -Encoding utf8 }
}

if (-not (Get-Process -Id $Id -ErrorAction SilentlyContinue)) {
    Say "no process $Id is running "
    exit 1
}

if ($Force) {
    Stop-Process -Id $Id -Force
    Say "PID $Id terminated "
    exit 0
}

if (-not $IsWindows) {
    Stop-Process -Id $Id
    Say "SIGTERM sent to PID $Id "
    exit 0
}

if (-not ('StrataConsoleCtrl' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class StrataConsoleCtrl {
    [DllImport("kernel32.dll", SetLastError = true)] public static extern bool FreeConsole();
    [DllImport("kernel32.dll", SetLastError = true)] public static extern bool AttachConsole(uint processId);
    [DllImport("kernel32.dll", SetLastError = true)] public static extern bool GenerateConsoleCtrlEvent(uint ctrlEvent, uint processGroupId);
}
'@
}

# The console of this script is not the console of the bot: let go of ours first, then take the target's.
# The message is written BEFORE the detach because the handles of the console are what die here, not the
# inherited pipes of a redirected caller.
Say "posting Ctrl+C to the console of PID $Id "

[void][StrataConsoleCtrl]::FreeConsole()
if (-not [StrataConsoleCtrl]::AttachConsole([uint32]$Id)) {
    $attachErr = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
    Say "AttachConsole($Id) failed with Win32 error $attachErr — this instance owns no console, so Ctrl+C cannot be delivered to it "
    exit 2
}

# CTRL_C_EVENT = 0, process group 0 = every process attached to this console. The target's own handler
# runs inside the target's process; this one may well die from the same event, which is harmless — the
# event has already been posted.
if (-not [StrataConsoleCtrl]::GenerateConsoleCtrlEvent(0, 0)) {
    $postErr = [System.Runtime.InteropServices.Marshal]::GetLastWin32Error()
    Say "GenerateConsoleCtrlEvent failed with Win32 error $postErr"
    exit 2
}

Say "the event is posted — verify it by the process being gone (poll Get-Process; Wait-Process throws when the process is already gone and reads like a timeout) and read the log: the last lines of a graceful stop are «stopped by signal true», «poller stopped true» and «stopped by signal »"
exit 0
