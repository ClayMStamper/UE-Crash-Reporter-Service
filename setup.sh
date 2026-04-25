#!/usr/bin/env bash
# setup.sh — first-time project setup
# Run once after cloning: bash setup.sh
set -euo pipefail

echo "==> Downloading Go dependencies..."
go mod tidy

echo "==> Verifying build..."
go build ./...

REMOTE="https://github.com/ClayMStamper/UE-Crash-Reporter-Service.git"

if [ ! -d ".git" ]; then
  echo "==> Initialising git repository..."
  git init -b main
  git add .
  git commit -m "feat: initial UE crash reporter service"
  echo "==> Git repo initialised."
else
  echo "==> Git repo already exists — skipping init."
fi

echo "==> Configuring remote 'origin'..."
if git remote | grep -q "^origin$"; then
  git remote set-url origin "$REMOTE"
else
  git remote add origin "$REMOTE"
fi
echo "    remote: $REMOTE"

echo "==> Pushing to GitHub..."
git push -u origin main

echo ""
echo "All done!"
echo "  Local:  go run ./cmd/server"
echo "  Docker: docker compose up -d"
