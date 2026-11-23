// Fixture builder for OOXML tests.
//
// Real .pptx files cannot be committed as readable fixtures: they are zips, diffs are
// useless, and a corrupted fixture tells you nothing about why it broke. Building them
// here means the XML is reviewable and each test can vary one thing.
//
// The structure below is the minimum PowerPoint accepts: content types, a
// presentation part, one master, one layout, one theme, and the slides.

import JSZip from 'jszip';

const NS = {
  p: 'http://schemas.openxmlformats.org/presentationml/2006/main',
  a: 'http://schemas.openxmlformats.org/drawingml/2006/main',
  r: 'http://schemas.openxmlformats.org/officeDocument/2006/relationships',
};

const DECL = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>';

export const CONTENT_TYPES = `${DECL}
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Default Extension="png" ContentType="image/png"/>
<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>
<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>
<Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>
<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>
</Types>`;

export const ROOT_RELS = `${DECL}
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="ppt/presentation.xml"/>
</Relationships>`;

function presentationXml(slideCount) {
  const ids = Array.from({ length: slideCount }, (_, index) =>
    `<p:sldId id="${256 + index}" r:id="rId${index + 2}"/>`).join('');
  return `${DECL}
<p:presentation xmlns:p="${NS.p}" xmlns:a="${NS.a}" xmlns:r="${NS.r}">
<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>
<p:sldIdLst>${ids}</p:sldIdLst>
<p:sldSz cx="12192000" cy="6858000"/>
<p:notesSz cx="6858000" cy="9144000"/>
</p:presentation>`;
}

function presentationRels(slideCount) {
  const slides = Array.from({ length: slideCount }, (_, index) =>
    `<Relationship Id="rId${index + 2}" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide${index + 1}.xml"/>`).join('');
  return `${DECL}
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="slideMasters/slideMaster1.xml"/>
${slides}
</Relationships>`;
}

const THEME = `${DECL}
<a:theme xmlns:a="${NS.a}" name="Test">
<a:themeElements>
<a:clrScheme name="Test">
<a:dk1><a:srgbClr val="000000"/></a:dk1>
<a:lt1><a:srgbClr val="FFFFFF"/></a:lt1>
<a:dk2><a:srgbClr val="1B2430"/></a:dk2>
<a:lt2><a:srgbClr val="F2F4F7"/></a:lt2>
<a:accent1><a:srgbClr val="1F5EFF"/></a:accent1>
<a:accent2><a:srgbClr val="C86400"/></a:accent2>
<a:accent3><a:srgbClr val="1A6B41"/></a:accent3>
<a:accent4><a:srgbClr val="B02A1F"/></a:accent4>
<a:accent5><a:srgbClr val="7C818B"/></a:accent5>
<a:accent6><a:srgbClr val="A06207"/></a:accent6>
<a:hlink><a:srgbClr val="1F5EFF"/></a:hlink>
<a:folHlink><a:srgbClr val="7C818B"/></a:folHlink>
</a:clrScheme>
<a:fontScheme name="Test"><a:majorFont><a:latin typeface="Calibri Light"/><a:ea typeface="微软雅黑"/></a:majorFont><a:minorFont><a:latin typeface="Calibri"/><a:ea typeface="微软雅黑"/></a:minorFont></a:fontScheme>
<a:fmtScheme name="Test"><a:fillStyleLst><a:solidFill><a:schemeClr val="accent1"/></a:solidFill></a:fillStyleLst><a:lnStyleLst><a:ln w="6350"><a:solidFill><a:schemeClr val="accent1"/></a:solidFill></a:ln></a:lnStyleLst><a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst><a:bgFillStyleLst><a:solidFill><a:schemeClr val="lt1"/></a:solidFill></a:bgFillStyleLst></a:fmtScheme>
</a:themeElements>
</a:theme>`;

const MASTER = `${DECL}
<p:sldMaster xmlns:p="${NS.p}" xmlns:a="${NS.a}" xmlns:r="${NS.r}">
<p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/></p:spTree></p:cSld>
<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>
<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst>
</p:sldMaster>`;

const MASTER_RELS = `${DECL}
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="../theme/theme1.xml"/>
</Relationships>`;

// The layout carries a positioned title placeholder. A slide that references the
// placeholder without its own transform inherits this geometry, which is exactly the
// case the editor must detect before it writes.
const LAYOUT = `${DECL}
<p:sldLayout xmlns:p="${NS.p}" xmlns:a="${NS.a}" xmlns:r="${NS.r}" type="titleOnly" preserve="1">
<p:cSld>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>
<p:sp><p:nvSpPr><p:cNvPr id="2" name="Title Placeholder"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:spPr><a:xfrm><a:off x="838200" y="365125"/><a:ext cx="10515600" cy="1325563"/></a:xfrm></p:spPr><p:txBody><a:bodyPr/><a:lstStyle/><a:p/></p:txBody></p:sp>
</p:spTree>
</p:cSld>
<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>
</p:sldLayout>`;

const LAYOUT_RELS = `${DECL}
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideMaster" Target="../slideMasters/slideMaster1.xml"/>
</Relationships>`;

export function titleSlideXml({ title = '季度复盘', body = '第一点\n第二点', withBody = true } = {}) {
  const bodyShape = withBody
    ? `<p:sp><p:nvSpPr><p:cNvPr id="3" name="正文 2"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="838200" y="1825625"/><a:ext cx="10515600" cy="4351338"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:pPr algn="l"/><a:r><a:rPr lang="zh-CN" sz="1800" b="0" dirty="0"><a:solidFill><a:srgbClr val="3D434D"/></a:solidFill><a:latin typeface="微软雅黑"/><a:ea typeface="微软雅黑"/></a:rPr><a:t>${body}</a:t></a:r></a:p></p:txBody></p:sp>`
    : '';
  return `${DECL}
<p:sld xmlns:p="${NS.p}" xmlns:a="${NS.a}" xmlns:r="${NS.r}">
<p:cSld>
<p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>
<p:sp><p:nvSpPr><p:cNvPr id="2" name="标题 1"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="zh-CN" sz="3200" b="1" dirty="0"><a:solidFill><a:srgbClr val="1B2430"/></a:solidFill><a:latin typeface="微软雅黑"/><a:ea typeface="微软雅黑"/></a:rPr><a:t>${title}</a:t></a:r></a:p></p:txBody></p:sp>
${bodyShape}
</p:spTree>
</p:cSld>
<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>
</p:sld>`;
}

export function tableSlideXml({ rows = 2, cols = 3 } = {}) {
  const grid = Array.from({ length: cols }, () => '<a:gridCol w="2000000"/>').join('');
  const body = Array.from({ length: rows }, () => {
    const cells = Array.from({ length: cols }, () =>
      '<a:tc><a:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="zh-CN" sz="1200"/><a:t>值</a:t></a:r></a:p></a:txBody><a:tcPr/></a:tc>').join('');
    return `<a:tr h="370840">${cells}</a:tr>`;
  }).join('');
  return `${DECL}
<p:sld xmlns:p="${NS.p}" xmlns:a="${NS.a}" xmlns:r="${NS.r}">
<p:cSld><p:spTree>
<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/>
<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="4" name="表格 3"/><p:cNvGraphicFramePr><a:graphicFrameLocks noGrp="1"/></p:cNvGraphicFramePr><p:nvPr/></p:nvGraphicFramePr><p:xfrm><a:off x="838200" y="1825625"/><a:ext cx="6000000" cy="741680"/></p:xfrm><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/table"><a:tbl><a:tblPr firstRow="1" bandRow="1"/><a:tblGrid>${grid}</a:tblGrid>${body}</a:tbl></a:graphicData></a:graphic></p:graphicFrame>
</p:spTree></p:cSld>
<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr>
</p:sld>`;
}

export const SLIDE_RELS = `${DECL}
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slideLayout" Target="../slideLayouts/slideLayout1.xml"/>
</Relationships>`;

/** Assemble the package. Extra slides are passed as raw XML strings. */
export async function buildPptx({ slides = [titleSlideXml()] } = {}) {
  const zip = new JSZip();
  zip.file('[Content_Types].xml', CONTENT_TYPES);
  zip.file('_rels/.rels', ROOT_RELS);
  zip.file('ppt/presentation.xml', presentationXml(slides.length));
  zip.file('ppt/_rels/presentation.xml.rels', presentationRels(slides.length));
  zip.file('ppt/theme/theme1.xml', THEME);
  zip.file('ppt/slideMasters/slideMaster1.xml', MASTER);
  zip.file('ppt/slideMasters/_rels/slideMaster1.xml.rels', MASTER_RELS);
  zip.file('ppt/slideLayouts/slideLayout1.xml', LAYOUT);
  zip.file('ppt/slideLayouts/_rels/slideLayout1.xml.rels', LAYOUT_RELS);
  slides.forEach((xml, index) => {
    zip.file(`ppt/slides/slide${index + 1}.xml`, xml);
    zip.file(`ppt/slides/_rels/slide${index + 1}.xml.rels`, SLIDE_RELS);
  });
  return zip.generateAsync({ type: 'uint8array' });
}

export { NS, DECL };
