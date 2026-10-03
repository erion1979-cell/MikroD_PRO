@echo off
title Inverter Monitor
cd /d "%~dp0"
rem The converter IP is set on the web page (Connection settings) and remembered.
python inverter_monitor.py
if errorlevel 1 (
  echo.
  echo Could not start. Is Python installed? Get it from https://www.python.org/downloads/
  echo During install, tick "Add python.exe to PATH".
)
pause
