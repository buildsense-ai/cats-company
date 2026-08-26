import { useEffect, useRef, useState, type CSSProperties } from 'react'
import { motion, useMotionValueEvent, useReducedMotion, useScroll, useTransform } from 'framer-motion'
import { Reveal } from './Reveal'
import { ResourceConvergenceScene } from './ResourceConvergence'

type WorkflowStoryData = {
  title: string
  body: string
  imageSide: 'left' | 'right'
  videoSrc?: string
  posterTime?: number
  videoFit?: 'contain' | 'raw'
  immersive?: 'copy-left' | 'copy-right'
}

const stories: WorkflowStoryData[] = [
  {
    title: '为每项工作，配置专属 AI 员工',
    body: '你可以为不同工作分别创建专属 AI 员工，设置它的名称、工作方向、需要使用的技能和运行方式。完成配置后，它会按照你确认的职责和工作边界处理对应任务，方便你按工作内容进行分工和管理。',
    imageSide: 'left',
    videoSrc: '/workflow-ai-employee.mp4',
    posterTime: 14,
    immersive: 'copy-left',
  },
  {
    title: '从目标到成果，全程清晰可见',
    body: '交付目标后，CatsCo 会先把任务拆成清晰步骤，整理当前进度和待确认事项，再调用需要的工具逐步推进。过程中每一步都能回看，完成后把报告、文件或其他成果带回当前会话，方便继续修改和使用。',
    imageSide: 'left',
    videoSrc: '/workflow-goal-delivery.mp4?v=3',
    posterTime: 5,
    videoFit: 'raw',
    immersive: 'copy-right',
  },
  {
    title: '一个 AI 员工，连接所有授权设备',
    body: '同一项任务可以在已授权且在线的电脑、服务器和云端环境中继续推进。CatsCo 会保留当前目标、已完成步骤和相关资料，减少切换设备、传递文件和重复补充背景的麻烦，让工作在不同环境之间自然接上。',
    imageSide: 'left',
    videoSrc: '/workflow-device-connection.mp4',
    posterTime: 5,
    videoFit: 'raw',
    immersive: 'copy-left',
  },
  {
    title: '工作成果，统一留在云端',
    body: '完成后的文件、摘要和需要确认的事项会统一回到当前任务。你可以在云端查看已交付内容，核对关键数字和口径，再继续修改或下载，需要时也能回到同一项任务继续使用，避免在不同设备之间反复寻找文件。',
    imageSide: 'left',
    videoSrc: '/workflow-cloud-handoff.mp4',
    posterTime: 5,
    videoFit: 'raw',
    immersive: 'copy-right',
  },
]

function formatVideoTime(value: number) {
  if (!Number.isFinite(value)) return '0:00'
  const seconds = Math.max(0, Math.floor(value))
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}

function WorkflowPreviewVideo({
  src,
  title,
  posterTime,
  fit,
}: {
  src: string
  title: string
  posterTime: number
  fit?: WorkflowStoryData['videoFit']
}) {
  const frameRef = useRef<HTMLDivElement>(null)
  const videoRef = useRef<HTMLVideoElement>(null)
  const hasStartedRef = useRef(false)
  const [isPlaying, setIsPlaying] = useState(false)
  const [currentTime, setCurrentTime] = useState(posterTime)
  const [duration, setDuration] = useState(0)
  const [metadataReady, setMetadataReady] = useState(false)
  const [isInView, setIsInView] = useState(false)
  const progressPercent = duration > 0
    ? Math.min(100, Math.max(0, (currentTime / duration) * 100))
    : 0

  useEffect(() => {
    const frame = frameRef.current
    if (!frame) return

    const observer = new IntersectionObserver(
      ([entry]) => setIsInView(entry.isIntersecting),
      { rootMargin: '120px 0px', threshold: 0.35 },
    )

    observer.observe(frame)
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    const video = videoRef.current
    if (!video || !metadataReady) return

    if (!isInView) {
      video.pause()
      return
    }

    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return

    if (!hasStartedRef.current) {
      hasStartedRef.current = true
      video.currentTime = 0
      setCurrentTime(0)
    }

    void video.play().catch(() => {
      setIsPlaying(false)
    })
  }, [isInView, metadataReady])

  const showPosterFrame = () => {
    const video = videoRef.current
    if (!video || !Number.isFinite(video.duration)) return
    const coverTime = Math.min(posterTime, Math.max(0, video.duration - 0.05))
    video.currentTime = coverTime
    setCurrentTime(coverTime)
  }

  const handleTogglePlayback = async () => {
    const video = videoRef.current
    if (!video) return

    if (!video.paused) {
      video.pause()
      return
    }

    if (!hasStartedRef.current) {
      hasStartedRef.current = true
      video.currentTime = 0
      setCurrentTime(0)
    }

    await video.play()
  }

  const handleLoadedMetadata = () => {
    const video = videoRef.current
    if (!video) return
    setDuration(video.duration)
    showPosterFrame()
    setMetadataReady(true)
  }

  return (
    <div
      ref={frameRef}
      className={`workflow-story-placeholder workflow-story-video-frame${fit ? ` workflow-story-video-frame-${fit}` : ''}`}
      data-playing={isPlaying}
      data-metadata-ready={metadataReady}
    >
      <video
        ref={videoRef}
        className={`workflow-story-video${fit ? ` workflow-story-video-${fit}` : ''}`}
        src={src}
        aria-label={`${title}演示视频`}
        disablePictureInPicture
        loop
        muted
        playsInline
        preload="metadata"
        onLoadedMetadata={handleLoadedMetadata}
        onPlay={() => setIsPlaying(true)}
        onPause={() => setIsPlaying(false)}
        onTimeUpdate={(event) => setCurrentTime(event.currentTarget.currentTime)}
      />

      <button
        className="workflow-video-start"
        type="button"
        aria-label={`播放${title}演示视频`}
        onClick={handleTogglePlayback}
      >
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d="m9 7 8 5-8 5Z" />
        </svg>
      </button>

      <div className="workflow-video-controls">
        <button
          type="button"
          aria-label={isPlaying ? '暂停演示视频' : '播放演示视频'}
          onClick={handleTogglePlayback}
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            {isPlaying ? (
              <path d="M8 7v10M16 7v10" />
            ) : (
              <path d="m9 7 8 5-8 5Z" />
            )}
          </svg>
        </button>
        <input
          type="range"
          min="0"
          max={duration || 1}
          step="0.05"
          value={Math.min(currentTime, duration || 1)}
          style={{ '--workflow-video-progress': `${progressPercent}%` } as CSSProperties}
          aria-label="视频播放进度"
          onChange={(event) => {
            const video = videoRef.current
            if (!video) return
            video.currentTime = Number(event.currentTarget.value)
            setCurrentTime(video.currentTime)
          }}
        />
        <span>{formatVideoTime(currentTime)} / {formatVideoTime(duration)}</span>
      </div>
    </div>
  )
}

function WorkflowStory({ story }: { story: WorkflowStoryData }) {
  const storyRef = useRef<HTMLElement>(null)
  const [copyVisible, setCopyVisible] = useState(false)

  useEffect(() => {
    const storyElement = storyRef.current
    if (!storyElement) return

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (!entry.isIntersecting) return
        setCopyVisible(true)
        observer.disconnect()
      },
      { rootMargin: '0px 0px -34% 0px', threshold: 0.12 },
    )

    observer.observe(storyElement)
    return () => observer.disconnect()
  }, [])

  return (
    <article
      ref={storyRef}
      className={`workflow-story workflow-story-${story.imageSide}`}
      data-copy-visible={copyVisible}
    >
      {story.videoSrc ? (
        <WorkflowPreviewVideo
          src={story.videoSrc}
          title={story.title}
          posterTime={story.posterTime ?? 0}
          fit={story.videoFit}
        />
      ) : (
        <div className="workflow-story-placeholder" aria-hidden="true" />
      )}
      <div className="workflow-story-copy">
        <h2>{story.title}</h2>
        <p>{story.body}</p>
      </div>
    </article>
  )
}

function ImmersiveWorkflowStory({ story }: { story: WorkflowStoryData }) {
  const storyRef = useRef<HTMLElement>(null)
  const previousPageYRef = useRef(0)
  const copySide = story.immersive ?? 'copy-right'
  const initialMediaWidth = story.videoFit ? '82%' : '72%'
  const initialMediaLeft = story.videoFit ? '9%' : '14%'
  const finalMediaLeft = copySide === 'copy-left' ? '54%' : '0%'
  const copyStartX = copySide === 'copy-left' ? -120 : 120
  const bodyStartX = copySide === 'copy-left' ? -88 : 88
  const reduceMotion = useReducedMotion() ?? false
  const [titleLocked, setTitleLocked] = useState(false)
  const [bodyLocked, setBodyLocked] = useState(false)
  const [isDesktop, setIsDesktop] = useState(() => (
    typeof window === 'undefined' ? true : window.matchMedia('(min-width: 769px)').matches
  ))
  const { scrollY, scrollYProgress } = useScroll({
    target: storyRef,
    offset: ['start start', 'end end'],
  })

  const mediaWidth = useTransform(
    scrollYProgress,
    [0, 0.3, 0.76, 1],
    [initialMediaWidth, initialMediaWidth, '46%', '46%'],
  )
  const mediaLeft = useTransform(
    scrollYProgress,
    [0, 0.3, 0.76, 1],
    [initialMediaLeft, initialMediaLeft, finalMediaLeft, finalMediaLeft],
  )
  const titleOpacity = useTransform(scrollYProgress, [0, 0.54, 0.74], [0, 0, 1])
  const titleX = useTransform(scrollYProgress, [0, 0.54, 0.76], [copyStartX, copyStartX, 0])
  const bodyOpacity = useTransform(scrollYProgress, [0, 0.66, 0.86], [0, 0, 1])
  const bodyX = useTransform(scrollYProgress, [0, 0.66, 0.88], [bodyStartX, bodyStartX, 0])
  const animateScroll = isDesktop && !reduceMotion

  useMotionValueEvent(scrollYProgress, 'change', (latest) => {
    const pageY = scrollY.get()
    const movingUp = pageY < previousPageYRef.current
    previousPageYRef.current = pageY

    if (!movingUp && latest >= 0.74) {
      setTitleLocked(true)
    } else if (movingUp && latest < 0.54) {
      setTitleLocked(false)
    }

    if (!movingUp && latest >= 0.86) {
      setBodyLocked(true)
    } else if (movingUp && latest < 0.66) {
      setBodyLocked(false)
    }
  })

  useEffect(() => {
    const mediaQuery = window.matchMedia('(min-width: 769px)')
    const updateLayout = () => setIsDesktop(mediaQuery.matches)

    updateLayout()
    mediaQuery.addEventListener('change', updateLayout)
    return () => mediaQuery.removeEventListener('change', updateLayout)
  }, [])

  return (
    <article
      ref={storyRef}
      className={`workflow-story workflow-story-immersive workflow-story-immersive-${copySide}`}
    >
      <div className="workflow-story-immersive-stage">
        <motion.div
          className="workflow-story-immersive-media"
          style={animateScroll ? { width: mediaWidth, left: mediaLeft } : undefined}
        >
          <motion.div
            className="workflow-story-immersive-media-content"
            initial={animateScroll ? { opacity: 0, scale: 0.965 } : false}
            whileInView={animateScroll ? { opacity: 1, scale: 1 } : undefined}
            viewport={{ amount: 0.42 }}
            transition={{ duration: 0.58, ease: [0.16, 1, 0.3, 1] }}
          >
            <WorkflowPreviewVideo
              src={story.videoSrc ?? ''}
              title={story.title}
              posterTime={story.posterTime ?? 0}
              fit={story.videoFit}
            />
          </motion.div>
        </motion.div>

        <div className="workflow-story-copy">
          <motion.h2 style={animateScroll ? {
            opacity: titleLocked ? 1 : titleOpacity,
            x: titleLocked ? 0 : titleX,
          } : undefined}>
            {story.title}
          </motion.h2>
          <motion.p style={animateScroll ? {
            opacity: bodyLocked ? 1 : bodyOpacity,
            x: bodyLocked ? 0 : bodyX,
          } : undefined}>
            {story.body}
          </motion.p>
        </div>
      </div>
    </article>
  )
}

export function WorkflowStories() {
  return (
    <>
      <ResourceConvergenceScene />
      <section id="workflows" className="workflow-stories" aria-label="CatsCo 工作流程说明">
        <div className="section-container">
          {stories.map((story, index) => story.immersive ? (
            <ImmersiveWorkflowStory key={story.title} story={story} />
          ) : (
            <Reveal key={story.title} delay={index * 70}>
              <WorkflowStory story={story} />
            </Reveal>
          ))}
        </div>
      </section>
    </>
  )
}
