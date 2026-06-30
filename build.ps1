# 一键交叉编译 Linux 二进制（在 Windows 上运行）
# 用法：powershell -ExecutionPolicy Bypass -File .\build.ps1
# 产物：sms-middleware-linux-amd64、sms-middleware-linux-arm64（无依赖，可直接丢 Linux）

$ErrorActionPreference = 'Stop'
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    $env:Path = "C:\Program Files\Go\bin;$env:Path"
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "未找到 go，请先安装 Go 或确认 C:\Program Files\Go\bin 存在"
}

foreach ($arch in 'amd64', 'arm64') {
    $env:GOOS = 'linux'
    $env:GOARCH = $arch
    $env:CGO_ENABLED = '0'
    $out = "sms-middleware-linux-$arch"
    go build -trimpath -o $out
    Write-Host "已生成 $out ($([math]::Round((Get-Item $out).Length / 1KB)) KB)"
}
Remove-Item Env:\GOOS, Env:\GOARCH, Env:\CGO_ENABLED -ErrorAction SilentlyContinue
Write-Host "完成。把任一二进制 + install.sh 传到 Linux 后运行: sudo bash install.sh"
