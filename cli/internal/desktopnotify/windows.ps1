$ErrorActionPreference = 'Stop'
try {
    # CREATE_NO_WINDOW has no console code page. Decode the redirected pipe
    # directly; setting Console.InputEncoding would call SetConsoleCP and fail.
    $reader = [IO.StreamReader]::new([Console]::OpenStandardInput(), [Text.Encoding]::UTF8)
    $request = $reader.ReadToEnd() | ConvertFrom-Json
    $appId = 'ai.eigenflux.notifications'
    $stubId = '{C743DF52-A840-40D1-9789-17F595E56A01}'
    $registration = 'HKCU:\Software\Classes\AppUserModelId\' + $appId
    $shortcut = Join-Path ([Environment]::GetFolderPath('Programs')) 'EigenFlux Notifications.lnk'
    if ($request.action -eq 'enable') {
        # Versioned files avoid stale icon caches; atomic creation lets account
        # loops share the same assets without exposing partially written files.
        function Save-BrandIcon([string]$directory, [string]$encoded) {
            $bytes = [Convert]::FromBase64String($encoded)
            $hash = [Security.Cryptography.SHA256]::Create()
            try { $digest = [BitConverter]::ToString($hash.ComputeHash($bytes)).Replace('-', '').ToLowerInvariant() }
            finally { $hash.Dispose() }
            $path = Join-Path $directory ($digest + '.png')
            if (-not [IO.File]::Exists($path)) {
                $temporary = $path + '.' + [Guid]::NewGuid().ToString('N')
                try {
                    [IO.File]::WriteAllBytes($temporary, $bytes)
                    try { [IO.File]::Move($temporary, $path) }
                    catch { if (-not [IO.File]::Exists($path)) { throw } }
                } finally { if ([IO.File]::Exists($temporary)) { [IO.File]::Delete($temporary) } }
            }
            return $path
        }
        $iconDirectory = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'EigenFlux\Notifications'
        [IO.Directory]::CreateDirectory($iconDirectory) | Out-Null
        $iconPNG = Save-BrandIcon $iconDirectory $request.icon_png
        # A Start shortcut carries the AUMID and stub CLSID. Protocol activation
        # opens the HTTPS target in the default browser without a COM server.
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Runtime.InteropServices.ComTypes;

namespace EigenFluxNotifications {
    [ComImport, Guid("00021401-0000-0000-C000-000000000046")]
    class ShellLink { }
    [ComImport, Guid("000214F9-0000-0000-C000-000000000046"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IShellLinkW {
        void GetPath(IntPtr file, int max, IntPtr data, int flags);
        void GetIDList(out IntPtr id);
        void SetIDList(IntPtr id);
        void GetDescription(IntPtr text, int max);
        void SetDescription([MarshalAs(UnmanagedType.LPWStr)] string text);
        void GetWorkingDirectory(IntPtr path, int max);
        void SetWorkingDirectory([MarshalAs(UnmanagedType.LPWStr)] string path);
        void GetArguments(IntPtr args, int max);
        void SetArguments([MarshalAs(UnmanagedType.LPWStr)] string args);
        void GetHotkey(out short key);
        void SetHotkey(short key);
        void GetShowCmd(out int command);
        void SetShowCmd(int command);
        void GetIconLocation(IntPtr path, int max, out int index);
        void SetIconLocation([MarshalAs(UnmanagedType.LPWStr)] string path, int index);
        void SetRelativePath([MarshalAs(UnmanagedType.LPWStr)] string path, int reserved);
        void Resolve(IntPtr window, int flags);
        void SetPath([MarshalAs(UnmanagedType.LPWStr)] string path);
    }
    [StructLayout(LayoutKind.Sequential, Pack=4)]
    struct PropertyKey { public Guid format; public uint id; }
    [StructLayout(LayoutKind.Explicit, Size=16)]
    struct PropVariant {
        [FieldOffset(0)] public ushort type;
        [FieldOffset(8)] public IntPtr value;
    }
    [ComImport, Guid("886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    interface IPropertyStore {
        uint GetCount(out uint count);
        uint GetAt(uint index, out PropertyKey key);
        uint GetValue(ref PropertyKey key, out PropVariant value);
        void SetValue(ref PropertyKey key, ref PropVariant value);
        void Commit();
    }
    public static class Registration {
        public static void Create(string target, string shortcut, string appId, string activator) {
            object item = new ShellLink();
            try {
                var link = (IShellLinkW)item;
                link.SetPath(target);
                link.SetArguments("version --short");
                link.SetDescription("EigenFlux");
                var store = (IPropertyStore)item;
                var key = new PropertyKey { format = new Guid("9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3"), id = 5 };
                var value = new PropVariant { type = 31, value = Marshal.StringToCoTaskMemUni(appId) };
                try { store.SetValue(ref key, ref value); }
                finally { Marshal.FreeCoTaskMem(value.value); }
                key.id = 26;
                value = new PropVariant { type = 72, value = Marshal.AllocCoTaskMem(16) };
                Marshal.StructureToPtr(new Guid(activator), value.value, false);
                try { store.SetValue(ref key, ref value); }
                finally { Marshal.FreeCoTaskMem(value.value); }
                store.Commit();
                ((IPersistFile)item).Save(shortcut, true);
            } finally { Marshal.FinalReleaseComObject(item); }
        }
    }
}
'@
        [EigenFluxNotifications.Registration]::Create($request.executable, $shortcut, $appId, $stubId)
        New-Item -Path $registration -Force | Out-Null
        New-ItemProperty -Path $registration -Name DisplayName -Value 'EigenFlux' -PropertyType String -Force | Out-Null
        New-ItemProperty -Path $registration -Name IconUri -Value $iconPNG -PropertyType String -Force | Out-Null
        New-ItemProperty -Path $registration -Name CustomActivator -Value $stubId -PropertyType String -Force | Out-Null
    }
    if (-not (Test-Path -LiteralPath $shortcut) -or -not (Test-Path -LiteralPath $registration)) {
        [Console]::Out.WriteLine('setup_required'); exit 1
    }
    [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
    [Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
    [Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
    $notifier = [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($appId)
    if ($notifier.Setting.ToString() -ne 'Enabled') {
        [Console]::Out.WriteLine('permission_denied'); exit 1
    }
    if ($request.action -eq 'show') {
        $xml = New-Object Windows.Data.Xml.Dom.XmlDocument
        $xml.LoadXml($request.xml)
        $toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
        $toast.Tag = $request.tag
        $toast.Group = 'EigenFlux'
        $notifier.Show($toast)
    } elseif ($request.action -ne 'check' -and $request.action -ne 'enable') {
        throw 'invalid action'
    }
    [Console]::Out.WriteLine('accepted')
} catch {
    # Do not expose notification data, browser URLs or arbitrary exception data.
    [Console]::Out.WriteLine('notification_failed')
    exit 1
}
