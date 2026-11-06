// Source inventory for the local icon review page; excludes tests and previews.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
const require = createRequire(import.meta.url);
const { parse } = require('@babel/parser');
const postcss = require('postcss');
const traverse = require('@babel/traverse').default;
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const sourceRoot = path.join(root, 'src');
const records = new Map();
const customIcons = { AppleBrand: '../widgets/apple-brand-icon' };
function files(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    if (entry.isDirectory()) return ['demos', 'dev', '__tests__'].includes(entry.name) ? [] : files(path.join(dir, entry.name));
    return /\.(jsx|js)$/.test(entry.name) && !/\.(test|spec)\./.test(entry.name) ? [path.join(dir, entry.name)] : [];
  });
}
function literal(node) {
  if (!node) return undefined;
  if (['StringLiteral', 'NumericLiteral', 'BooleanLiteral'].includes(node.type)) return node.value;
  if (node.type === 'JSXExpressionContainer') return literal(node.expression);
  return undefined;
}
function strings(node) {
  if (!node) return [];
  if (node.type === 'StringLiteral') return [node.value];
  if (node.type === 'JSXExpressionContainer') return strings(node.expression);
  if (node.type === 'ConditionalExpression') return [...strings(node.consequent), ...strings(node.alternate)];
  if (node.type === 'LogicalExpression') return strings(node.right);
  if (node.type === 'TemplateLiteral') return [node.quasis.map(part => part.value.cooked).join(' '), ...node.expressions.flatMap(strings)];
  return [];
}
for (const file of files(sourceRoot)) {
  const code = fs.readFileSync(file, 'utf8');
  const ast = parse(code, {sourceType:'module',plugins:['jsx']});
  const imports = new Map();
  for (const node of ast.program.body) {
    if (node.type !== 'ImportDeclaration') continue;
    if (node.source.value.endsWith('/apple-brand-icon')) {
      const spec = node.specifiers.find(s => s.type === 'ImportDefaultSpecifier');
      if (spec) {
        imports.set(spec.local.name, 'AppleBrand');
        if (!records.has('AppleBrand')) records.set('AppleBrand', {name:'AppleBrand',defaultProps:{size:24,strokeWidth:1.5},files:[],usages:[]});
        records.get('AppleBrand').files.push(path.relative(sourceRoot,file).replaceAll('\\','/'));
      }
    }
    if (node.source.value !== 'lucide-react') continue;
    for (const spec of node.specifiers) {
      if (spec.type !== 'ImportSpecifier') continue;
      const name=spec.imported.name;
      imports.set(spec.local.name,name);
      if (!records.has(name)) records.set(name,{name,files:[],usages:[]});
      records.get(name).files.push(path.relative(sourceRoot,file).replaceAll('\\','/'));
    }
  }
  if (!imports.size) continue;
  function resolve(p, seen=new Set()) {
    if (!p?.node || seen.has(p.node)) return [];
    seen.add(p.node);
    const n=p.node;
    if (n.type==='Identifier'||n.type==='JSXIdentifier') {
      if(imports.has(n.name))return [imports.get(n.name)];
      const binding=p.scope.getBinding(n.name);
      if(binding?.path.isVariableDeclarator())return resolve(binding.path.get('init'),seen);
      // Local array maps used for theme and platform icon choices.
      const fn=binding?.path.findParent(x=>x.isFunction());
      if(fn?.parentPath.isCallExpression()&&fn.parentPath.node.callee.type==='MemberExpression'&&fn.parentPath.node.callee.property.name==='map')return resolve(fn.parentPath.get('callee.object'),seen);
      return [];
    }
    if(n.type==='ConditionalExpression')return [...resolve(p.get('consequent'),seen),...resolve(p.get('alternate'),seen)];
    if(n.type==='LogicalExpression')return [...resolve(p.get('left'),seen),...resolve(p.get('right'),seen)];
    if(n.type==='MemberExpression')return resolve(p.get('object'),seen);
    if(n.type==='ObjectExpression')return p.get('properties').flatMap(prop=>prop.isObjectProperty()?resolve(prop.get('value'),seen):[]);
    if(n.type==='ArrayExpression')return p.get('elements').flatMap(e=>resolve(e,seen));
    return [];
  }
  traverse(ast,{JSXElement(p){
    const names=[...new Set(resolve(p.get('openingElement.name')))];
    if(!names.length)return;
    const attrs=Object.fromEntries(p.node.openingElement.attributes.filter(a=>a.type==='JSXAttribute').map(a=>[a.name.name,a.value]));
    const props={};
    for(const name of ['size','strokeWidth','fill','color','stroke','absoluteStrokeWidth']){
      const value=literal(attrs[name]);if(value!==undefined)props[name]=value;
    }
    const classes=[...new Set(strings(attrs.className))].filter(Boolean);
    const contexts=[];
    for(let parent=p.parentPath;parent&&contexts.length<4;parent=parent.parentPath){
      if(!parent.isJSXElement())continue;
      const cls=parent.node.openingElement.attributes.find(a=>a.type==='JSXAttribute'&&a.name.name==='className');
      if(cls)contexts.push(strings(cls.value).join(' ').trim());
    }
    const fillOptions=strings(attrs.fill);
    const propVariants=props.fill===undefined&&fillOptions.length?fillOptions.map(fill=>({...props,fill})):undefined;
    const usage={file:path.relative(sourceRoot,file).replaceAll('\\','/'),line:p.node.loc.start.line,props,...(propVariants?{propVariants}:{}),classes,contexts};
    for(const name of names)records.get(name)?.usages.push(usage);
  }});
}
const icons=[...records.values()].sort((a,b)=>a.name.localeCompare(b.name));
for(const record of icons){
  record.files=[...new Set(record.files)].sort();
  record.usages=[...new Map(record.usages.map(u=>[`${u.file}:${u.line}`,u])).values()];
}
const out=path.join(sourceRoot,'demos');fs.mkdirSync(out,{recursive:true});
fs.writeFileSync(path.join(out,'icon-preview-data.json'),JSON.stringify({icons},null,2)+'\n');
fs.writeFileSync(path.join(out,'icon-preview-icons.js'),`// Generated by scripts/generate-icon-preview.mjs.\nimport { ${icons.filter(i=>!customIcons[i.name]).map(i=>i.name).join(', ')} } from 'lucide-react';\n${icons.filter(i=>customIcons[i.name]).map(i=>`import ${i.name} from '${customIcons[i.name]}';`).join('\n')}\nexport default { ${icons.map(i=>i.name).join(', ')} };\n`);
const composer=fs.readFileSync(path.join(sourceRoot,'widgets/chat-composer.jsx'),'utf8');
const composerAst=parse(composer,{sourceType:'module',plugins:['jsx']});
const voiceParts=composerAst.program.body.flatMap(node=>{
  const declaration=node.type==='ExportNamedDeclaration'?node.declaration:node;
  const name=declaration?.id?.name||declaration?.declarations?.[0]?.id?.name;
  return ['voiceWavePhaseSpeed','voiceWavePath','VoiceHoldWave'].includes(name)?[composer.slice(declaration.start,declaration.end)]:[];
});
fs.writeFileSync(path.join(out,'icon-preview-voice.jsx'),`// Generated unchanged declarations from widgets/chat-composer.jsx.\nimport React, { useEffect, useRef } from 'react';\nimport { Mic } from 'lucide-react';\nconst VOICE_WAVE_RAMP_MS = 2200;\n${voiceParts.join('\n\n')}\nexport default VoiceHoldWave;\n`);
const knowledgeCss=postcss.parse(fs.readFileSync(path.join(sourceRoot,'views/knowledge-wiki-view.css'),'utf8'));
const knowledgeRules=knowledgeCss.nodes.filter(n=>(n.type==='rule'&&n.selector==='.spin')||(n.type==='atrule'&&n.name==='keyframes'&&n.params==='cc-knowledge-spin')||(n.type==='atrule'&&n.name==='media'&&n.nodes?.some(rule=>rule.selector==='.spin'))).map(n=>n.toString().replace(/\.spin(?=\s*\{)/g,'.ip-knowledge-spin'));
fs.writeFileSync(path.join(out,'icon-preview-source.css'),`/* Generated from views/knowledge-wiki-view.css; scoped to this preview. */\n${knowledgeRules.join('\n')}\n`);
console.log(`${icons.length} icons, ${icons.reduce((n,i)=>n+i.usages.length,0)} JSX references, ${new Set(icons.flatMap(i=>i.files)).size} source files`);
