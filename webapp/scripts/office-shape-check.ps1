# Prove an inserted picture is a real picture, not a broken-image placeholder.
#
# This check exists because `office-docx-open-check.ps1` cannot judge it: Word opens a
# document whose image relationship or media part is wrong, shows a broken-image box,
# and reports no error at all. `RESULT OPENED_CLEAN` therefore proves nothing about the
# picture. The only honest signal is asking Word how many inline shapes it sees and what
# type and size they are.
#
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File office-shape-check.ps1 <path.docx>

param(
  [Parameter(Mandatory = $true)][string]$Path
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $Path)) {
  "RESULT MISSING $Path"
  exit 2
}

$word = New-Object -ComObject Word.Application
$word.Visible = $false
$word.DisplayAlerts = 0
try {
  # Read-only open: this script must never modify the file it inspects.
  $doc = $word.Documents.Open($Path, $false, $true)
  "RESULT OPENED"
  "inlineShapes $($doc.InlineShapes.Count)"
  for ($i = 1; $i -le $doc.InlineShapes.Count; $i++) {
    $shape = $doc.InlineShapes.Item($i)
    # Type 3 is wdInlineShapePicture. Anything else means Word did not resolve it as a picture.
    "shape $i type=$($shape.Type) w=$([math]::Round($shape.Width, 1)) h=$([math]::Round($shape.Height, 1))"
  }
  "tables $($doc.Tables.Count)"
  for ($t = 1; $t -le $doc.Tables.Count; $t++) {
    $table = $doc.Tables.Item($t)
    $first = $table.Cell(1, 1).Range.Text.Trim()
    "table$t rows=$($table.Rows.Count) cols=$($table.Columns.Count) cell11=[$first]"
  }
  $doc.Close(0)
} finally {
  $word.Quit()
  [System.Runtime.InteropServices.Marshal]::ReleaseComObject($word) | Out-Null
}
