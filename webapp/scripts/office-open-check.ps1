# Opens a .pptx through the real PowerPoint COM object and reports whether it loads
# without a repair prompt, plus what text each shape actually contains.
#
# Written as a file rather than an inline command because the quoting rules for inline
# PowerShell inside cmd /c mangle `$` and quotes, and a half-mangled automation script
# produces misleading "open failed" results.
#
# -ReadFormat additionally reads font size, bold and colour back out of each text shape.
# Opening clean only proves the XML is well formed. It does not prove the style landed on
# the run the user sees, so a style change needs that second assertion.

param(
  [Parameter(Mandatory = $true)][string]$Path,
  [string]$Expect = '',
  [switch]$ReadFormat,
  # Prints each shape's position and size as PowerPoint itself reports them, in points.
  # A geometry write is only proven by Office reading the new coordinates back; our own
  # parser agreeing with itself proves nothing about whether Office accepted the edit.
  [switch]$ReadGeometry
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $Path)) {
  Write-Output "RESULT MISSING_FILE $Path"
  exit 1
}

$app = $null
try {
  $app = New-Object -ComObject PowerPoint.Application
  # ReadOnly = msoTrue keeps the file untouched on disk, including its timestamp.
  $pres = $app.Presentations.Open($Path, $true, $false, $false)
  Write-Output "RESULT OPENED_CLEAN"
  Write-Output ("slides " + $pres.Slides.Count)

  $allText = @()
  for ($i = 1; $i -le $pres.Slides.Count; $i++) {
    $slide = $pres.Slides.Item($i)
    for ($j = 1; $j -le $slide.Shapes.Count; $j++) {
      $shape = $slide.Shapes.Item($j)
      $text = ''
      try {
        if ($shape.HasTextFrame -eq -1) {
          if ($shape.TextFrame.HasText -eq -1) {
            $text = $shape.TextFrame.TextRange.Text
          }
        }
      } catch { }
      $text = ($text -replace "`r", '|' -replace "`n", '|')
      Write-Output ("shape slide={0} idx={1} type={2} text={3}" -f $i, $j, $shape.Type, $text)
      if ($text) { $allText += $text }

      if ($ReadGeometry) {
        try {
          # PowerPoint reports Left/Top/Width/Height in points, not EMU. 914400 EMU per
          # inch over 72 points per inch gives 12700 EMU per point, so callers can convert.
          Write-Output ("geom slide={0} idx={1} left={2} top={3} width={4} height={5} type={6}" -f `
            $i, $j, [math]::Round($shape.Left, 2), [math]::Round($shape.Top, 2), `
            [math]::Round($shape.Width, 2), [math]::Round($shape.Height, 2), $shape.Type)
        } catch {
          Write-Output ("geom slide={0} idx={1} UNREADABLE {2}" -f $i, $j, $_.Exception.Message)
        }
      }

      if ($ReadFormat -and $text) {
        try {
          $range = $shape.TextFrame.TextRange
          $font = $range.Font
          # Bg is the background colour index, which is meaningless on a transparent
          # fill; only report RGB when a foreground colour is actually set.
          $colorIndex = $objColor = $null
          try { $colorIndex = $font.Color.RGB } catch { $colorIndex = $null }
          try { $objColor = $font.ColorType } catch { $objColor = $null }
          $hex = 'n/a'
          if ($null -ne $colorIndex) {
            # COM returns BGR as a signed 32-bit int; mask and swap to get RRGGBB.
            $bgr = [int64]$colorIndex
            $b = ($bgr -shr 16) -band 0xFF
            $g = ($bgr -shr 8) -band 0xFF
            $r = $bgr -band 0xFF
            $hex = ('#{0:X2}{1:X2}{2:X2}' -f $r, $g, $b)
          }
          Write-Output ("format slide={0} idx={1} size={2} bold={3} italic={4} color={5} colorType={6}" -f `
            $i, $j, $font.Size, $font.Bold, $font.Italic, $hex, $objColor)
        } catch {
          Write-Output ("format slide={0} idx={1} UNREADABLE {2}" -f $i, $j, $_.Exception.Message)
        }
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

  $pres.Close()
} catch {
  Write-Output ("RESULT OPEN_FAILED " + $_.Exception.Message)
} finally {
  if ($app) {
    try { $app.Quit() } catch { }
    try { [System.Runtime.Interopservices.Marshal]::ReleaseComObject($app) | Out-Null } catch { }
  }
}
