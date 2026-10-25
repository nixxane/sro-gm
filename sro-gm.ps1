<#
===========================================================================

sro-gm.ps1 - create a GM test character on a local OpenSRO world

Copies sro-gm/main.go into the OpenSRO checkout's git-ignored
apps/server/.state/sro-gm/ and runs it from apps/server (Go lets only code
inside the OpenSRO server module import its internal packages).

  .\sro-gm.ps1 -OpenSRO C:\opensro -Restart -account gm -name "[GM]Test"

-Restart stops the fleet first and redeploys afterwards, so the GM grant
is live when it finishes. Without it, stop the GameWorld yourself.
Every other argument goes to the tool; run with -help to list them.

===========================================================================
#>
param(
	[string]$OpenSRO = ".",
	[switch]$Restart,
	[Parameter(ValueFromRemainingArguments = $true)][string[]]$ToolArgs
)

# Native commands log on stderr; Windows PowerShell 5.1 would turn those
# lines into errors under "Stop". Exit codes decide failure instead.
$ErrorActionPreference = "Continue"
$server = Join-Path (Resolve-Path $OpenSRO) "apps\server"
if (-not (Test-Path (Join-Path $server "go.mod"))) {
	throw "$OpenSRO is not an OpenSRO checkout (no apps\server\go.mod). Pass -OpenSRO <path>."
}
$target = Join-Path $server ".state\sro-gm"
New-Item -ItemType Directory -Force $target | Out-Null
Copy-Item (Join-Path $PSScriptRoot "sro-gm\main.go") (Join-Path $target "main.go") -Force

Push-Location $server
try {
	if ($Restart) {
		Write-Host "Stopping the fleet..."
		go run ./cmd/operations/sro-nomad stop
		if ($LASTEXITCODE -ne 0) { throw "sro-nomad stop failed" }
	}
	go run ./.state/sro-gm @ToolArgs
	$toolExit = $LASTEXITCODE
	if ($Restart) {
		# Redeploy even after a refusal, so the world never stays down.
		Write-Host "Redeploying..."
		go run ./cmd/operations/sro-nomad deploy -build
		if ($LASTEXITCODE -ne 0) { throw "sro-nomad deploy failed" }
	}
	if ($toolExit -ne 0) { exit $toolExit }
}
finally {
	Pop-Location
}
