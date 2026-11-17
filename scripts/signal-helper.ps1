# signal-helper.ps1 — start a console process in its own process group and
# deliver CTRL_BREAK to it (Go runtime maps CTRL_BREAK to SIGINT/os.Interrupt).
# Why: taskkill without /F only works for GUI windows; GenerateConsoleCtrlEvent
# targets a group created with CREATE_NEW_PROCESS_GROUP.
param()

Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;

public static class WinSignal {
    [StructLayout(LayoutKind.Sequential)]
    public struct STARTUPINFO {
        public int cb;
        public IntPtr lpReserved, lpDesktop, lpTitle;
        public int dwX, dwY, dwXSize, dwYSize, dwXCountChars, dwYCountChars, dwFillAttribute, dwFlags;
        public short wShowWindow, cbReserved2;
        public IntPtr lpReserved2, hStdInput, hStdOutput, hStdError;
    }

    [StructLayout(LayoutKind.Sequential)]
    public struct PROCESS_INFORMATION {
        public IntPtr hProcess, hThread;
        public int dwProcessId, dwThreadId;
    }

    [DllImport("kernel32.dll", SetLastError = true, CharSet = CharSet.Ansi)]
    public static extern bool CreateProcess(
        string lpApplicationName, string lpCommandLine,
        IntPtr lpProcessAttributes, IntPtr lpThreadAttributes,
        bool bInheritHandles, uint dwCreationFlags,
        IntPtr lpEnvironment, string lpCurrentDirectory,
        ref STARTUPINFO lpStartupInfo, out PROCESS_INFORMATION lpProcessInformation);

    [DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool GenerateConsoleCtrlEvent(uint dwCtrlEvent, uint dwProcessGroupId);

    [DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool AttachConsole(uint dwProcessId);

    [DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool FreeConsole();

    [DllImport("kernel32.dll")]
    public static extern uint GetConsoleCP();

    public const uint CREATE_NEW_PROCESS_GROUP = 0x00000200;
    public const uint CREATE_NO_WINDOW = 0x08000000;
    public const uint CTRL_BREAK_EVENT = 1;

    public static bool HasConsole() { return GetConsoleCP() != 0; }

    // Starts exe in a new process group; returns child pid or -errno.
    public static int Start(string exe, string args, string cwd) {
        uint flags = CREATE_NEW_PROCESS_GROUP;
        // If this process has no console the child would have none either and
        // could never receive a ctrl event: give it its own (invisible) console.
        if (!HasConsole()) flags |= CREATE_NO_WINDOW;
        var si = new STARTUPINFO();
        si.cb = Marshal.SizeOf(typeof(STARTUPINFO));
        PROCESS_INFORMATION pi;
        string cmd = "\"" + exe + "\" " + args;
        if (!CreateProcess(null, cmd, IntPtr.Zero, IntPtr.Zero, false,
                           flags, IntPtr.Zero, cwd, ref si, out pi)) {
            return -Marshal.GetLastWin32Error();
        }
        return pi.dwProcessId;
    }

    // Delivers CTRL_BREAK to the child's process group.
    // needAttach: child owns a private console (created with CREATE_NO_WINDOW)
    // while this process has none -> attach first, then signal, then detach.
    public static bool SignalBreak(uint pid, bool needAttach) {
        if (needAttach && !AttachConsole(pid)) return false;
        bool ok = GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT, pid);
        if (needAttach) FreeConsole();
        return ok;
    }
}
"@

function Start-InGroup {
    param([string]$Exe, [string]$Args, [string]$Cwd)
    $pid2 = [WinSignal]::Start($Exe, $Args, $Cwd)
    if ($pid2 -le 0) { throw "CreateProcess failed: $pid2" }
    return $pid2
}

function Send-Break {
    param([int]$TargetPid)
    $needAttach = -not [WinSignal]::HasConsole()
    $ok = [WinSignal]::SignalBreak([uint32]$TargetPid, $needAttach)
    if (-not $ok) { throw "GenerateConsoleCtrlEvent failed: $([Runtime.InteropServices.Marshal]::GetLastWin32Error())" }
}
