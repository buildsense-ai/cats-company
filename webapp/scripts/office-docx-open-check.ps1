# Opens a .docx through the real Word COM object and reports whether it loads without a repair
# prompt, plus the text and formatting of each paragraph.
#
# Written as a file for the same reason as the PowerPoint check: inline PowerShell inside cmd /c
# mangles `$` and quotes, and a half-mangled automation script reports misleading failures.

param(
  [Parameter(Mandatory = $true)][string]$Path,
  [string]$Expect = '',
  [switch]$ReadFormat
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $Path)) {
  Write-Output "RESULT MISSING_FILE $Path"
  exit 1
}

$word = $null
try {
  $word = New-Object -ComObject Word.Application
  # Visible = false keeps the check headless; ReadOnly keeps the file untouched on disk.
  $word.Visible = $false
  # A first-run privacy or activation dialog blocks COM silently and looks like a hang.
  # Suppressing alerts is what makes this check runnable unattended.
  $word.DisplayAlerts = 0
  $word.AutomationSecurity = 3
  $doc = $word.Documents.Open($Path, $false, $true, $false)
  Write-Output "RESULT OPENED_CLEAN"
  Write-Output ("paragraphs " + $doc.Paragraphs.Count)

  Write-Output ("tables " + $doc.Tables.Count)
  for ($tableIndex = 1; $tableIndex -le $doc.Tables.Count; $tableIndex++) {
    $table = $doc.Tables.Item($tableIndex)
    Write-Output ("table idx={0} rows={1} cols={2}" -f $tableIndex, $table.Rows.Count, $table.Columns.Count)
  }

  $allText = @()
  for ($i = 1; $i -le $doc.Paragraphs.Count; $i++) {
    $para = $doc.Paragraphs.Item($i)
    $text = $para.Range.Text
    $text = ($text -replace "`r", '' -replace "`n", '|' -replace "`a", '')
    Write-Output ("para idx={0} style={1} text={2}" -f $i, $para.Style.NameLocal, $text)
    if ($text) { $allText += $text }

    if ($ReadFormat -and $text) {
      try {
        $font = $para.Range.Font
        $color = $null
        try { $color = $font.Color } catch { $color = $null }
        $hex = 'n/a'
        if ($null -ne $color -and $color -ne 9999999) {
          # Word returns BGR as a signed int; mask and swap to RRGGBB.
          $bgr = [int64]$color
          $b = ($bgr -shr 16) -band 0xFF
          $g = ($bgr -shr 8) -band 0xFF
          $r = $bgr -band 0xFF
          $hex = ('#{0:X2}{1:X2}{2:X2}' -f $r, $g, $b)
        }
        Write-Output ("format idx={0} size={1} bold={2} italic={3} color={4} align={5}" -f `
          $i, $font.Size, $font.Bold, $font.Italic, $hex, $para.Alignment)
      } catch {
        Write-Output ("format idx={0} UNREADABLE {1}" -f $i, $_.Exception.Message)
      }
    }
  }

  if ($Expect) {
    $joined = $allText -join ' '
    if ($joined -like "*$Expect*") {
      Write-Output "EXPECT FOUND $Expect"
    } else {
      Write-Output "EXPECT MISSING $Expect"
    }
  }

  $doc.Close($false)
} catch {
  Write-Output ("RESULT OPEN_FAILED " + $_.Exception.Message)
} finally {
  if ($word) {
    try { $word.Quit() } catch { }
    try { [System.Runtime.Interopservices.Marshal]::ReleaseComObject($word) | Out-Null } catch { }
  }
}
