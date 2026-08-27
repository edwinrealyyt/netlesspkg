# NetlessPkg 多平台构建脚本 (PowerShell)
# 用法: .\build.ps1 [-Clean]

param([switch]$Clean)

$Version = "v0.1.0"
$App = "netlesspkg"
$OutputDir = "dist"

if ($Clean) {
    Write-Host "清理构建产物..."
    Remove-Item -Recurse -Force $OutputDir -ErrorAction SilentlyContinue
    exit 0
}

$Platforms = @(
    @{ GOOS="linux";   GOARCH="amd64"; Ext="" },
    @{ GOOS="linux";   GOARCH="arm64"; Ext="" },
    @{ GOOS="linux";   GOARCH="arm";   Ext="" },
    @{ GOOS="windows"; GOARCH="amd64"; Ext=".exe" }
)

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

foreach ($p in $Platforms) {
    $output = "$OutputDir/${App}_$($p.GOOS)_$($p.GOARCH)$($p.Ext)"
    Write-Host "构建 $($p.GOOS)/$($p.GOARCH) -> $output"

    $env:CGO_ENABLED = "0"
    $env:GOOS = $p.GOOS
    $env:GOARCH = $p.GOARCH
    go build -ldflags="-s -w" -o $output .

    if ($LASTEXITCODE -ne 0) {
        Write-Host "构建失败: $($p.GOOS)/$($p.GOARCH)" -ForegroundColor Red
        exit 1
    }
}

# 恢复环境变量
Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue

Write-Host ""
Write-Host "构建完成:"
Get-ChildItem $OutputDir | Format-Table Name, Length
