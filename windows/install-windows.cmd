@echo off
rem Double-click or run: install-windows.cmd -Call kn6fln -From "KN6FLN <you@example.com>"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0install-windows.ps1" %*
pause
