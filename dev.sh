#!/bin/bash
cd "$(dirname "$0")"
lsof -ti :8080 | xargs kill -9 2>/dev/null
pkill -f "naroom" 2>/dev/null
sleep 1
rm -f naroom.db naroom.db-shm naroom.db-wal
DEV_MODE=true SERVER_SALT=devsalt go run -tags dev ./cmd/naroom/main.go
