<#
  Run by the MSI (deferred, as SYSTEM) after the Passage service exists. Restricts %ProgramData%\Passage and the config registry key
  to SYSTEM, Administrators and the service account, so the database password and the service signing keys are not readable by
  ordinary users. Safe to run again.
#>
param(
    [Parameter(Mandatory)] [string] $DataDir,
    [Parameter(Mandatory)] [string] $Account
)
$ErrorActionPreference = 'Stop'

function New-Rule($identity, $rights) {
    $inherit = [System.Security.AccessControl.InheritanceFlags]'ContainerInherit,ObjectInherit'
    New-Object System.Security.AccessControl.FileSystemAccessRule($identity, $rights, $inherit, 'None', 'Allow')
}

$acl = Get-Acl -LiteralPath $DataDir
$acl.SetAccessRuleProtection($true, $false)
foreach ($rule in @($acl.Access)) { [void]$acl.RemoveAccessRule($rule) }
$acl.AddAccessRule((New-Rule 'NT AUTHORITY\SYSTEM' 'FullControl'))
$acl.AddAccessRule((New-Rule 'BUILTIN\Administrators' 'FullControl'))
$acl.AddAccessRule((New-Rule $Account 'Modify'))
Set-Acl -LiteralPath $DataDir -AclObject $acl

$key = 'HKLM:\SOFTWARE\Passage\Config'
if (Test-Path $key) {
    $racl = Get-Acl -Path $key
    $racl.SetAccessRuleProtection($true, $false)
    foreach ($rule in @($racl.Access)) { [void]$racl.RemoveAccessRule($rule) }
    foreach ($who in 'NT AUTHORITY\SYSTEM', 'BUILTIN\Administrators') {
        $racl.AddAccessRule((New-Object System.Security.AccessControl.RegistryAccessRule($who, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')))
    }
    $racl.AddAccessRule((New-Object System.Security.AccessControl.RegistryAccessRule($Account, 'ReadKey', 'ContainerInherit,ObjectInherit', 'None', 'Allow')))
    Set-Acl -Path $key -AclObject $racl
}
Write-Output "ACL set on $DataDir and $key for $Account"
