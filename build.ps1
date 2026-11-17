# Cross-compile static, dependency-free binaries for every supported target into dist\.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot
$version = if ($env:VERSION) { $env:VERSION } else { '0.2.0' }
New-Item -ItemType Directory -Force dist | Out-Null
$targets = 'linux/amd64','linux/arm64','linux/riscv64','linux/arm','darwin/arm64','darwin/amd64','windows/amd64','windows/arm64','freebsd/amd64'
foreach ($t in $targets) {
  $os, $arch = $t.Split('/')
  $ext = if ($os -eq 'windows') { '.exe' } else { '' }
  $out = "dist/argus-$os-$arch$ext"
  $env:CGO_ENABLED = '0'; $env:GOOS = $os; $env:GOARCH = $arch; $env:GOARM = '7'
  go build -trimpath -ldflags "-s -w -X main.version=$version" -o $out .
  if ($LASTEXITCODE) { throw "build failed for $t" }
  '{0,-36} {1,6:N1} MB' -f $out, ((Get-Item $out).Length / 1MB)
}
Remove-Item Env:GOOS, Env:GOARCH, Env:GOARM, Env:CGO_ENABLED
