# setup.ps1 — first-time project setup
# Run once after cloning: .\setup.ps1

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

Write-Host "==> Downloading Go dependencies..." -ForegroundColor Cyan
go mod tidy

Write-Host "==> Verifying build..." -ForegroundColor Cyan
go build ./...

$remote = "https://github.com/ClayMStamper/UE-Crash-Reporter-Service.git"

if (-not (Test-Path ".git")) {
    Write-Host "==> Initialising git repository..." -ForegroundColor Cyan
    git init -b main
    git add .
    git commit -m "feat: initial UE crash reporter service"
    Write-Host "==> Git repo initialised." -ForegroundColor Green
} else {
    Write-Host "==> Git repo already exists — skipping init." -ForegroundColor Yellow
}

Write-Host "==> Configuring remote 'origin'..." -ForegroundColor Cyan
$existing = git remote 2>$null
if ($existing -contains "origin") {
    git remote set-url origin $remote
} else {
    git remote add origin $remote
}
Write-Host "    remote: $remote" -ForegroundColor Gray

Write-Host "==> Pushing to GitHub..." -ForegroundColor Cyan
git push -u origin main

Write-Host ""
Write-Host "All done! To run locally:" -ForegroundColor Green
Write-Host '  go run ./cmd/server' -ForegroundColor White
Write-Host ""
Write-Host "To run with Docker:" -ForegroundColor Green
Write-Host "  docker compose up -d" -ForegroundColor White
