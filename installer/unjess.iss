; =====================================================================
; Unjess - Inno Setup Script
; Clean, standard, non-heuristic Windows Installer for Unjess Desktop
; =====================================================================

#define MyAppName "Unjess"
#define MyAppVersion "2.9.9"
#define MyAppPublisher "Unjess AI"
#define MyAppURL "https://unjess.si"
#define MyAppExeName "Unjess.exe"

[Setup]
; Unique GUID so Windows recognizes upgrades and cleanly replaces old versions
AppId={{E681C701-44B9-4C49-BF2E-311D79E69A45}}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
AppUpdatesURL={#MyAppURL}

; Install to %LOCALAPPDATA%\Programs\Unjess (same directory as existing installs, no admin rights required)
DefaultDirName={localappdata}\Programs\Unjess
DisableDirPage=yes
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes

; Non-administrative install (avoids UAC prompt, matches user-scoped desktop apps like VS Code)
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog

; Output file setup
OutputDir=..\dist
OutputBaseFilename=Unjess-Setup
SetupIconFile=..\daemon\cmd\installer\unjess_icon.ico
UninstallDisplayIcon={app}\{#MyAppExeName}
UninstallDisplayName={#MyAppName}

; Compression
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern

; Close running Unjess processes before updating
CloseApplications=yes
CloseApplicationsFilter=Unjess.exe,unjessd.exe,unjess-cli.exe

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"
Name: "startupicon"; Description: "Start Unjess Daemon on Windows startup"; GroupDescription: "Startup:"

[Files]
; Desktop shell (Rust Wry wrapper)
Source: "..\dist\Unjess.exe"; DestDir: "{app}"; Flags: ignoreversion
; Core Go Daemon
Source: "..\dist\unjessd.exe"; DestDir: "{app}"; Flags: ignoreversion
; Go CLI (njss / unjess-cli)
Source: "..\dist\unjess-cli.exe"; DestDir: "{app}"; Flags: ignoreversion
; WebView2 Runtime Loader DLL (required by Wry / Tao on Windows)
Source: "..\dist\WebView2Loader.dll"; DestDir: "{app}"; Flags: ignoreversion
; Application Icon
Source: "..\daemon\cmd\installer\unjess_icon.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
; Start Menu shortcuts
Name: "{group}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\unjess_icon.ico"
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"
; Desktop shortcut (optional)
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; IconFilename: "{app}\unjess_icon.ico"; Tasks: desktopicon
; Windows Startup shortcut (optional)
Name: "{userstartup}\Unjess Daemon"; Filename: "{app}\unjessd.exe"; Parameters: "--silent"; IconFilename: "{app}\unjess_icon.ico"; Tasks: startupicon

[Registry]
; Add CLI to user PATH environment variable
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "Path"; ValueData: "{olddata};{app}"; Check: NeedsAddPath(ExpandConstant('{app}'))

[Run]
; Option to launch Unjess immediately after installation finishes (run as original user to avoid UIPI drag-and-drop blocking)
Filename: "{app}\{#MyAppExeName}"; Description: "{cm:LaunchProgram,{#StringChange(MyAppName, '&', '&&')}}"; Flags: nowait postinstall skipifsilent runasoriginaluser

[Code]
// Helper function to check if {app} is already in the user PATH variable
function NeedsAddPath(Param: string): boolean;
var
  OrigPath: string;
begin
  if not RegQueryStringValue(HKEY_CURRENT_USER, 'Environment', 'Path', OrigPath)
  then begin
    Result := True;
    exit;
  end;
  // Look for the path with semicolons
  Result := Pos(';' + UpperCase(Param) + ';', ';' + UpperCase(OrigPath) + ';') = 0;
end;
