# Seven-Air-Link RC: Windows x64 native build (MSYS2 UCRT64 + SQLite).
# Does NOT download packages; dependencies must be installed deliberately.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
Set-Location $PSScriptRoot
if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) { throw 'Run on Windows.' }
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw 'Go is missing. Install current supported Go for Windows.' }
if ((go env GOHOSTARCH).Trim() -ne 'amd64') { throw 'This build script requires native Windows x64; do not pretend arm64 is a native x64 cgo build.' }
$prefix = if ($env:SAL_UCRT64) { $env:SAL_UCRT64 } else { 'C:\msys64\ucrt64' }
$gcc = Join-Path $prefix 'bin\gcc.exe'
$header = Join-Path $prefix 'include\sqlite3.h'
$lib = Join-Path $prefix 'lib'
$dll = Join-Path $prefix 'bin\libsqlite3-0.dll'
if (-not (Test-Path $gcc) -or -not (Test-Path $header) -or -not (Test-Path $dll)) {
  throw @"
MSYS2 UCRT64 GCC/SQLite missing under $prefix.
Install MSYS2, open 'MSYS2 UCRT64' terminal and run:
  pacman -Syu
  pacman -S --needed mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-sqlite3
Then rerun this script in PowerShell.
"@
}
$env:PATH = (Join-Path $prefix 'bin') + ';' + $env:PATH
$env:CC = $gcc
$env:CGO_ENABLED = '1'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_CFLAGS = '-I' + ($prefix -replace '\\','/') + '/include'
$env:CGO_LDFLAGS = '-L' + ($prefix -replace '\\','/') + '/lib'
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
Write-Host "Building Seven-Air-Link on Windows/amd64 using $(go version) and $gcc"
New-Item -ItemType Directory -Force -Path dist | Out-Null
go test ./...
if ($LASTEXITCODE -ne 0) { throw 'Native Go tests failed.' }
$out = Join-Path (Get-Location) 'dist\seven-air-link-windows-amd64.exe'
go build -buildvcs=false -trimpath -ldflags='-s -w' -o $out ./cmd/seven-air-link
if ($LASTEXITCODE -ne 0) { throw 'Go build failed.' }
# Copy SQLite plus any further non-system DLL dependencies from UCRT64.
# MSYS2 updates can change SQLite's transitive DLL requirements. Inspect the
# compiled executable and each copied DLL, rather than assuming one DLL is enough.
$objdump = Join-Path $prefix 'bin\objdump.exe'
if (-not (Test-Path $objdump)) { throw 'MSYS2 binutils objdump.exe is required to verify runtime DLL dependencies.' }
$queue = New-Object 'System.Collections.Generic.Queue[string]'
$seen = New-Object 'System.Collections.Generic.HashSet[string]' ([System.StringComparer]::OrdinalIgnoreCase)
$queue.Enqueue($out)
while ($queue.Count -gt 0) {
  $item = $queue.Dequeue()
  $deps = & $objdump -p $item 2>&1
  if ($LASTEXITCODE -ne 0) { throw "Dependency inspection failed: $item" }
  foreach ($line in $deps) {
    if ($line -match 'DLL Name:\s*(\S+)') {
      $dep = $Matches[1]
      $source = Join-Path $prefix ('bin\' + $dep)
      if (Test-Path -LiteralPath $source) {
        if ($seen.Add($dep)) {
          Copy-Item -LiteralPath $source -Destination (Join-Path 'dist' $dep) -Force
          $queue.Enqueue($source)
        }
      }
    }
  }
}
if (-not (Test-Path './dist/libsqlite3-0.dll')) { throw 'Missing runtime SQLite DLL after dependency walk.' }
Copy-Item -LiteralPath $out -Destination './seven-air-link.exe' -Force
Get-ChildItem -Path dist -Filter '*.dll' | ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination (Join-Path '.' $_.Name) -Force }
& './seven-air-link.exe' version
if ($LASTEXITCODE -ne 0) { throw 'Runtime smoke test failed (SQLite DLL / CRT). Check runtime DLL dependencies.' }
Get-FileHash -Algorithm SHA256 './seven-air-link.exe' | Format-List
Write-Host 'SUCCESS: .\seven-air-link.exe (keep all dist/*.dll beside the EXE)'
Write-Host 'Next: .\seven-air-link.exe join https://MAC_IP:7777 --fingerprint SERVER_SHA256 --name Windows-Dev --config .\windows-node.json'
