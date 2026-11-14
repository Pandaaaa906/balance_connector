@echo off
if not exist build mkdir build
go build -ldflags="-s -w" -o ./build/ ./balance_connector
