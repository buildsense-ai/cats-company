# feat: add editable PPT/Word sidebar previews with persisted new-version saves

## Summary

This change adds local, in-browser PPTX and DOCX sidebar previews with direct text, style, geometry, table, image, grouping, z-order, slide/page, undo/redo, and Word paragraph/table editing. Saving uploads edited bytes as a new immutable attachment message instead of mutating the original, then retargets the open preview to the saved version. It also fixes inherited-layout writes so a layout-wide geometry edit updates the layout part shared by all using slides, and makes the save notice explicitly state what 小八 reads next.

Validation boundary: this was tested as **local frontend code + production API/data**, not as a deployed build and not against a local backend. The production backend behavior is therefore part of the observed result, while the changed frontend code is the code under test.

## Test Results

Acceptance report: `docs/office-editor/ACCEPTANCE-REPORT.md`

Gate summary: L0 `8 files / 166 tests / 0 failed`; L1 build `EXIT=0`; L2–L5 all pass. The handoff baseline was 155 tests; the current tree has 166. This is documented as 11 additional regression cases, including the B18 and B19 defenses, with no tests skipped or deleted.

| ID | Status | Evidence |
|---|---|---|
| T-01 | PASS | PPT probe: 2 slides, 39 parts, 3x3 table, no NaN |
| T-02 | PASS | 37145 -> 35057 bytes; text edited; 1100 leading + 3565 trailing chars unchanged; reparse OK |
| T-03 | PASS | PowerPoint OPENED_CLEAN; 2 slides; EXPECT FOUND 验证写入成功 |
| T-04 | PASS | PowerPoint reads 33pt, bold=-1, #C86400 |
| T-05 | PASS | 88.8/54.4/487.2/44.8pt; drift 0 |
| T-06 | PASS | 4 slides, 44 parts, sldIds 257/256/258/259, group 2 children, image1.png 70 bytes |
| T-07 | PASS | PowerPoint OPENED_CLEAN; 4 slides and geometry readable |
| T-08 | PASS | Group child relative/absolute delta 365760,182880; group and sibling unchanged |
| T-09 | PASS | Child 57.6,334.8 -> 86.4,349.2; sibling 252,334.8; slide 3 unchanged |
| T-10 | PASS | Word 4 paragraphs; 18pt, bold, #C86400, centered |
| T-11 | PASS | sample-table.docx; Metric -> CELL-EDITED; 2x3; rId6; 2160000 EMU |
| T-12 | PASS | Word OPENED_CLEAN; 2x3 table; EXPECT FOUND CELL-EDITED |
| T-13 | PASS | InlineShapes 1, type 3, 170.1 x 170.1pt; edited table cell correct |
| T-14 | PASS | PPT/Word cards render and preview; legacy .ppt disabled with 暂不支持预览; download preserved |
| T-15 | PASS | Canvas, list, hover, and blank-click selection checks all true |
| T-16 | PASS | Shift adds without replacing; second Shift removes; group enabled |
| T-17 | PASS | Slow drag no drift; live panel factor; NW opposite corner fixed; clamp/min/no-negative pass |
| T-18 | PASS | Text/style/geometry immediate updates; all six page-align buttons verified |
| T-19 | PASS | Dialog has both scopes; only-page leaves other page unchanged; layout-wide moves both; file parsed |
| T-20 | PASS | 7 inserts; image1.png and image2.png; shape IDs 1..10 unique; table grid 3 |
| T-21 | PASS | Group box stable; child selected/moved +20,+10; sibling/group fixed; Office opened clean |
| T-22 | PASS | Target id 7 index moved 4->5 and 4->3; non-shape props stay first; files open clean |
| T-23 | PASS | Duplicate/add/delete/move and last-page rejection verified |
| T-24 | PASS | Undo twice; redo branch cleared; one drag = one history; 65 edits collapse to 限值 4 |
| T-25 | PASS | Word paragraph/style/indent/table cell/image edits; last paragraph deletion rejected |
| T-26 | PASS | Real bot task: save notice exact, attachment count 1->2, reopen edited, bot returned T26-L5-BOT; Office clean |
| T-27 | PASS | Empty-byte media and upload cases rejected (2 tests) |
| T-28 | PASS | XML escaping for &, <, > (3 tests) |
| T-29 | PASS | xml:space=preserve keeps leading spaces (1 test) |
| T-30 | PASS | Non-pptx and non-docx packages rejected (2 tests) |
| T-31 | PASS | Mixed slide/element batch rejected (1 test) |
| T-32 | PASS | Two structural operations rejected (1 test) |
| T-33 | PASS | Unknown operation rejected (1 test) |
| T-34 | PASS | Collapsed canvas returns 0, not NaN/Infinity (1 test) |
| T-35 | PASS | 20/40/60 MB loaded clean in 1631/3341/4460 ms |
| T-36 | PASS | Moderate, long, and unbroken Chinese text stay inside canvas without horizontal overflow |

## NOT RUN

None. T-01 through T-36 and exception paths were run; evidence is in the acceptance report.

One initial L5 attempt used a standard group with no bot member (`memberCount: 1`), so no reply could arrive. That was a test-context issue, not a product failure. The valid rerun used the real bot task “24小时天选打工仔” and completed all five steps.

## Known Gaps

- G2 remains: multi-element align/distribute have library and unit coverage but no UI entry. The verified UI scope is single-element page alignment.
- G3 remains: PPT table-cell text editing is not implemented; Word table-cell editing is implemented and verified.
- G4 remains: `/uploads/` authorization is existing production-backend behavior and was not changed.
- G5 is measured for 20/40/60 MB samples; this does not guarantee every document at the 60 MB boundary.
- G6 partially remains: Chinese text did not overflow the canvas, but line-break points may still differ from Office.
- G7 remains: long-term knowledge-base archival and maintenance are outside this code acceptance.

## Acceptance Fixes

- B18: `layoutGeometry` now resolves the slide layout and writes the validated layout part instead of falling through to the slide writer. Defense: `writes inherited geometry to the layout for every slide that uses it`.
- B19: the save notice is centralized as `OFFICE_SAVE_NOTICE` and now states that the new attachment is the saved version and what 小八 reads. Defense: `keeps the save notice explicit about the new attachment and what 小八 reads`.
