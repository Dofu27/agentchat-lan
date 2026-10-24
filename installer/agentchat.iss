#define MyAppName "AgentChat LAN"
#ifndef AppVersion
  #define AppVersion "0.30.33"
#endif
#ifndef StageDir
  #define StageDir "..\build\inno"
#endif

[Setup]
AppId={{8D20A77B-6F0B-4B6E-9A10-7C68B9D3F1A2}
AppName={#MyAppName}
AppVersion={#AppVersion}
AppVerName={#MyAppName} {#AppVersion}
AppPublisher=豆腐
AppPublisherURL=https://github.com/Dofu27/agentchat-lan
AppSupportURL=https://github.com/Dofu27/agentchat-lan
AppUpdatesURL=https://github.com/Dofu27/agentchat-lan/releases
DefaultDirName={autopf}\AgentChat Console
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
OutputDir=..\dist
OutputBaseFilename=AgentChatConsole-v{#AppVersion}-setup
SetupIconFile=..\assets\agentchat.ico
UninstallDisplayIcon={app}\AgentChatConsole.exe
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"
Name: "chinesesimplified"; MessagesFile: "ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务:"

[Files]
Source: "{#StageDir}\AgentChatConsole.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#StageDir}\agentchat.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#StageDir}\MicrosoftEdgeWebView2RuntimeInstallerX64.exe"; DestDir: "{tmp}"; Flags: dontcopy

[Icons]
Name: "{group}\AgentChat Console"; Filename: "{app}\AgentChatConsole.exe"
Name: "{group}\卸载 AgentChat LAN"; Filename: "{uninstallexe}"
Name: "{autodesktop}\AgentChat Console"; Filename: "{app}\AgentChatConsole.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\AgentChatConsole.exe"; Description: "启动 AgentChat Console（需要管理员）"; Flags: shellexec nowait postinstall skipifsilent

[Code]
var
  RolePage: TInputOptionWizardPage;
  HubPage: TInputQueryWizardPage;

function WebView2Installed(): Boolean;
begin
  Result :=
    DirExists(ExpandConstant('{commonpf32}\Microsoft\EdgeWebView\Application')) or
    DirExists(ExpandConstant('{commonpf}\Microsoft\EdgeWebView\Application')) or
    DirExists(ExpandConstant('{localappdata}\Microsoft\EdgeWebView\Application'));
end;

procedure InitializeWizard;
begin
  RolePage := CreateInputOptionPage(wpSelectDir,
    '选择 Console 角色', '这台机器将如何使用 AgentChat？',
    '角色可以在安装后从 Console 的设置顶部修改。',
    True, False);
  RolePage.Add('Hub 主机');
  RolePage.Add('聊天客户端');
  RolePage.Add('观察者');
  RolePage.Values[0] := True;

  HubPage := CreateInputQueryPage(RolePage.ID,
    'Hub 地址', '客户端连接哪个 Hub？',
    '如果暂时不确定，可以留空，稍后在 Console 中加入。');
  HubPage.Add('Hub URL:', False);
end;

function SelectedRole(): String;
begin
  if RolePage.Values[0] then Result := 'hub'
  else if RolePage.Values[1] then Result := 'client'
  else Result := 'observer';
end;

function JsonEscape(Value: String): String;
var
  I: Integer;
  C: Char;
begin
  Result := '';
  for I := 1 to Length(Value) do
  begin
    C := Value[I];
    if C = #92 then
      Result := Result + '\\'
    else if C = #34 then
      Result := Result + '\"'
    else
      Result := Result + C;
  end;
end;

procedure SaveDesktopConfig();
var
  ConfigDir, ConfigPath, Json, Role, HubURL, ExePath: String;
begin
  ConfigDir := ExpandConstant('{userappdata}\AgentChat');
  ConfigPath := ConfigDir + '\desktop.json';
  if FileExists(ConfigPath) then
    exit;

  ForceDirectories(ConfigDir);
  Role := SelectedRole();
  HubURL := Trim(HubPage.Values[0]);
  ExePath := JsonEscape(ExpandConstant('{app}\agentchat.exe'));

  Json := '{' + #13#10 +
    '  "title": "AgentChat Console",' + #13#10 +
    '  "width": 1180,' + #13#10 +
    '  "height": 820,' + #13#10 +
    '  "role": "' + Role + '",' + #13#10 +
    '  "agentchat_path": "' + ExePath + '",' + #13#10 +
    '  "setup_done": false';
  if HubURL <> '' then
    Json := Json + ',' + #13#10 + '  "hub_url": "' + JsonEscape(HubURL) + '"';
  Json := Json + #13#10 + '}' + #13#10;

  SaveStringToFile(ConfigPath, Json, False);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Result := '';
  if CompareText(ExpandConstant('{app}'), ExpandConstant('{autopf}\AgentChat')) = 0 then
  begin
    Result := '该目录由 AgentChat Hub 服务使用。请选择 AgentChat Console 专用目录，避免覆盖服务二进制。';
    exit;
  end;
  if WebView2Installed() then
    exit;

  ExtractTemporaryFile('MicrosoftEdgeWebView2RuntimeInstallerX64.exe');
  if not Exec(ExpandConstant('{tmp}\MicrosoftEdgeWebView2RuntimeInstallerX64.exe'),
    '/silent /install', '', SW_SHOWNORMAL, ewWaitUntilTerminated, ResultCode) then
  begin
    Result := '无法启动 WebView2 安装器。';
    exit;
  end;
  if not WebView2Installed() then
    Result := 'WebView2 安装未完成，请检查网络/权限后重试。';
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    SaveDesktopConfig();
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  // Configuration and Hub data are intentionally preserved.
end;
