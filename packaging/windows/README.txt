ARTEX for Windows

Open ARTEX from the Start menu. Your browser opens the local ARTEX app;
on first launch, follow the setup page to connect to local PostgreSQL.

Installation is private to your Windows user and needs no administrator:
  %LOCALAPPDATA%\Programs\ARTEX
Your configuration and application data are stored separately:
  %LOCALAPPDATA%\ARTEX
Upgrading or uninstalling the application preserves this data.

To stop the local app explicitly, run in PowerShell:
  & "$env:LOCALAPPDATA\Programs\ARTEX\artex.exe" launch stop

Install a newer MSI to upgrade. Older versions are blocked. To create an
optional desktop shortcut, pass DESKTOP_SHORTCUT=1 to msiexec during install.
Uninstall through Windows Settings > Apps, or msiexec /x <installer.msi>.

config.example.json is a reference; it is not your saved configuration.
PostgreSQL is not included. Install PostgreSQL separately if needed.
