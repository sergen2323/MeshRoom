; MeshRoom — установщик Windows (Inno Setup 6).
; Собирается в CI: iscc /DAppVersion=x.y.z build\windows\meshroom.iss
; Рядом должны лежать: dist\meshroom.exe и build\windows\wintun.dll (x64).

#ifndef AppVersion
  #define AppVersion "0.2.0"
#endif

[Setup]
AppId={{7E9A2C64-3F1B-4A57-9D8E-C0FFEE000001}
AppName=MeshRoom
AppVersion={#AppVersion}
AppPublisher=MeshRoom
DefaultDirName={autopf}\MeshRoom
DefaultGroupName=MeshRoom
UninstallDisplayIcon={app}\meshroom.exe
OutputBaseFilename=MeshRoom-{#AppVersion}-windows-setup
OutputDir=..\..\dist
Compression=lzma2
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
SetupIconFile=icon.ico
WizardStyle=modern
; туннель требует прав администратора — ставим для всех
PrivilegesRequired=admin

[Languages]
Name: "russian"; MessagesFile: "compiler:Languages\Russian.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
Source: "..\..\dist\meshroom.exe"; DestDir: "{app}"; Flags: ignoreversion
; Wintun: официальный подписанный DLL с wintun.net (кладётся в CI)
Source: "wintun.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "icon.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\MeshRoom"; Filename: "{app}\meshroom.exe"; IconFilename: "{app}\icon.ico"
Name: "{autodesktop}\MeshRoom"; Filename: "{app}\meshroom.exe"; IconFilename: "{app}\icon.ico"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"

[Run]
Filename: "{app}\meshroom.exe"; Description: "{cm:LaunchProgram,MeshRoom}"; Flags: nowait postinstall skipifsilent

[UninstallRun]
; закрыть приложение перед удалением
Filename: "taskkill"; Parameters: "/im meshroom.exe /f"; Flags: runhidden; RunOnceId: "KillApp"
