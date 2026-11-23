$ErrorActionPreference = 'Stop'
$path = $args[0]
$ppt = New-Object -ComObject PowerPoint.Application
$pres = $ppt.Presentations.Open($path, $true, $false, $false)
try {
  "slides $($pres.Slides.Count)"
  foreach ($slide in $pres.Slides) {
    foreach ($shape in $slide.Shapes) {
      if ($shape.Type -ne 6) { continue }
      "group slide=$($slide.SlideIndex) name=$($shape.Name) left=$([math]::Round($shape.Left,1)) top=$([math]::Round($shape.Top,1)) items=$($shape.GroupItems.Count)"
      foreach ($item in $shape.GroupItems) {
        "  item name=$($item.Name) type=$($item.Type) left=$([math]::Round($item.Left,1)) top=$([math]::Round($item.Top,1)) w=$([math]::Round($item.Width,1)) h=$([math]::Round($item.Height,1))"
      }
    }
  }
} finally {
  $pres.Close()
  $ppt.Quit()
  [System.Runtime.InteropServices.Marshal]::ReleaseComObject($ppt) | Out-Null
}
