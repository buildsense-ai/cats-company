import React, { useEffect, useRef, useState } from 'react';
import { Pause, Play, RotateCcw, RotateCw } from 'lucide-react';

export function videoTimeLabel(value) {
  const seconds = Math.max(0, Number(value) || 0);
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${(seconds % 60).toFixed(2).padStart(5, '0')}`;
}

// Keep transport controls outside the selectable video pixels. The same video
// element survives mode changes, preserving its decoded frame and playhead.
export default function VideoFilePreview({ src, label = '视频预览', annotating = false, onError }) {
  const videoRef = useRef(null);
  const [time, setTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [ready, setReady] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => { if (annotating) videoRef.current?.pause(); }, [annotating]);
  const sync = event => {
    const video = event.currentTarget;
    setTime(Number.isFinite(video.currentTime) ? video.currentTime : 0);
    setDuration(Number.isFinite(video.duration) ? video.duration : 0);
    setReady(video.readyState >= 2);
  };
  const seek = value => {
    const video = videoRef.current;
    if (!video || !Number.isFinite(video.duration) || video.duration <= 0) return;
    video.pause();
    video.currentTime = Math.max(0, Math.min(Number(value), video.duration));
    setTime(video.currentTime);
    setError('');
  };
  const togglePlayback = async () => {
    const video = videoRef.current;
    if (!video) return;
    if (!video.paused) { video.pause(); return; }
    try { await video.play(); setError(''); }
    catch { setError('暂时无法播放，请重试或调整进度。'); }
  };
  return <div className="file-annotation-video" data-annotating={annotating || undefined}>
    <div className="file-annotation-video-stage">
      <video ref={videoRef} src={src} data-file-annotation-surface="video" controls={!annotating}
        playsInline preload="auto" aria-label={label}
        onLoadedMetadata={sync} onLoadedData={sync} onDurationChange={sync} onTimeUpdate={sync}
        onPlay={() => setPlaying(true)} onPause={() => setPlaying(false)} onEnded={() => setPlaying(false)}
        onSeeking={() => setWaiting(true)} onSeeked={event => { sync(event); setWaiting(false); }}
        onWaiting={() => setWaiting(true)} onCanPlay={event => { sync(event); setWaiting(false); }}
        onError={() => { setError('视频加载失败，请重试或下载原文件。'); onError?.(); }} />
      {annotating && (!ready || waiting) && <span className="file-annotation-video-status" role="status">
        {waiting ? '正在加载所选画面…' : '正在加载视频…'}
      </span>}
    </div>
    {annotating && <div className="file-annotation-video-transport" aria-label="视频播放控制">
      <div className="file-annotation-video-timeline">
        <input type="range" min="0" max={duration || 1} step="0.01" value={Math.min(time, duration || 1)}
          disabled={!duration} aria-label="视频播放进度" aria-valuetext={videoTimeLabel(time)}
          onChange={event => seek(event.currentTarget.value)} />
      </div>
      <div className="file-annotation-video-buttons">
        <button type="button" aria-label={playing ? '暂停视频' : '播放视频'} disabled={!ready}
          onClick={() => void togglePlayback()}>{playing ? <Pause size={16} /> : <Play size={16} />}</button>
        <button type="button" aria-label="后退 1 秒" disabled={!ready || time <= 0} onClick={() => seek(time - 1)}><RotateCcw size={15} /></button>
        <button type="button" aria-label="前进 1 秒" disabled={!ready || time >= duration} onClick={() => seek(time + 1)}><RotateCw size={15} /></button>
        <span className="file-annotation-video-time"><strong>{videoTimeLabel(time)}</strong><span> / {videoTimeLabel(duration)}</span></span>
        <span className="file-annotation-video-help">暂停后选择画面</span>
      </div>
    </div>}
    {error && <p className="file-annotation-video-error" role="alert">{error}</p>}
  </div>;
}
