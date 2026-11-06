import React, { useId } from 'react';

const roundedBox = (width, height, [tl, tr, br, bl]) =>
  `M${tl} 0H${width - tr}Q${width} 0 ${width} ${tr}V${height - br}Q${width} ${height} ${width - br} ${height}H${bl}Q0 ${height} 0 ${height - bl}V${tl}Q0 0 ${tl} 0Z`;

const bubbles = [
  { x: 1, y: 2, width: 49, height: 32, corners: [7, 7, 7, 2] },
  { x: 39, y: 22, width: 49, height: 32, corners: [7, 7, 2, 7] },
  { x: 10, y: 44, width: 40, height: 27, corners: [7, 7, 7, 2] },
];
const sheets = [0, 1, 2].map(index => ({ x: index * 13, y: index * 12, width: 76, height: 54, corners: [7, 7, 7, 7] }));
const delay = index => ({ animationDelay: `calc(${index} * var(--cc-loading-stagger))` });

// Cut the separation out of the rear layers so it reveals the actual surface,
// including translucent panels and moving liquid backgrounds, without painting a rim.
export default function LayeredLoadingMotion({ kind }) {
  const id = useId().replace(/:/g, '');
  const chat = kind === 'chat';
  const layers = chat ? bubbles : sheets;
  const width = chat ? 88 : 104;
  const height = chat ? 76 : 78;
  return <svg className={chat ? 'cc-history-loading-bubbles' : 'cc-load-window-stack'} width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true">
    <defs>
      {layers.slice(0, -1).map((_, index) => <mask key={index} id={`${id}-layer-${index}`} maskUnits="userSpaceOnUse" x="-4" y="-4" width={width + 8} height={height + 8} style={{ maskType: 'luminance' }}>
        <rect x="-4" y="-4" width={width + 8} height={height + 8} fill="white" />
        {layers.slice(index + 1).map((layer, offset) => <g key={offset} transform={`translate(${layer.x} ${layer.y})`}>
          <path className="cc-loading-layer-cutout" style={delay(index + offset + 1)} d={roundedBox(layer.width, layer.height, layer.corners)} fill="black" stroke="black" strokeWidth="4" strokeLinejoin="round" />
        </g>)}
      </mask>)}
    </defs>
    {layers.map((layer, index) => <g key={index} mask={index < layers.length - 1 ? `url(#${id}-layer-${index})` : undefined}>
      <g transform={`translate(${layer.x} ${layer.y})`}>
        <g className={chat ? `cc-history-loading-bubble bubble-${index}` : `cc-load-stack-sheet sheet-${index}`} style={delay(index)}>
          <path d={roundedBox(layer.width, layer.height, layer.corners)} />
          {!chat && <g className="cc-load-stack-bar">{[0, 1, 2].map(dot => <circle key={dot} cx={7 + dot * 5} cy="6" r="1" />)}</g>}
          <g className={chat ? 'cc-history-loading-lines' : 'cc-load-stack-content'}>
            {[0, 1].map(line => <rect key={line} className="cc-loading-layer-line" style={delay(index)} x="10" y={(chat ? (layer.height - 11) / 2 : 22) + line * (chat ? 8 : 9)} width={chat ? (index === 2 ? (line ? 13 : 20) : (line ? 18 : 26)) : (line ? 28 : 44)} height="3" rx="1.5" />)}
          </g>
        </g>
      </g>
    </g>)}
  </svg>;
}
