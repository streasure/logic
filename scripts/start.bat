@echo off
REM logicserver launcher (Windows).
REM Usage:
REM   scripts\start.bat                                                          normal config
REM   scripts\start.bat config\logicserver_stress.yaml config\tlog_stress.yaml   stress config
REM Keep this file pure ASCII: cmd.exe parses batch files with the active
REM console codepage, and multibyte characters break the parser.
cd /d "%~dp0\.."
if not exist bin mkdir bin
go build -o bin\logicserver.exe .\cmd\logicserver
if errorlevel 1 exit /b 1
set CONF=%1
set LOGGER=%2
if "%CONF%"=="" set CONF=config\logicserver.yaml
if "%LOGGER%"=="" set LOGGER=config\tlog.yaml
bin\logicserver.exe -conf %CONF% -logger %LOGGER%
