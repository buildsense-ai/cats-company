import React, { useMemo, useState } from 'react';
import { ChevronRight, Download, FileText, X } from 'lucide-react';
import PwaDownloadLink from './pwa-download-link';

export const TUTORIAL_TASKS = [
  {
    id: 'read-image',
    title: '读图提取信息',
    intro: '下载一张示例图片，让 CatsCo 读取图片内容并整理出清晰要点。',
    files: [
      { name: 'catsco-tutorial-sample.png', url: '/demo-artifacts/catsco-tutorial-sample.png' },
    ],
    prompt: '请在我的下载文件夹中找到“catsco-tutorial-sample.png”，读取这张图片的内容，并帮我整理成清晰的要点。请生成一份简短说明，包含：图片里主要有什么、可以提取出的文字或信息、以及你建议我如何使用这份信息。',
  },
  {
    id: 'move-image',
    title: '移动文件到桌面',
    intro: '下载同一张示例图片，让 CatsCo 在本机下载目录找到它，并安全移动到桌面。',
    files: [
      { name: 'catsco-tutorial-sample.png', url: '/demo-artifacts/catsco-tutorial-sample.png' },
    ],
    prompt: '请在我的下载文件夹中找到“catsco-tutorial-sample.png”，把它移动到桌面。完成后告诉我你移动前后的文件位置。如果桌面上已经有同名文件，请不要覆盖，改用一个安全的新文件名。',
  },
];

export function normalizeTutorialTask(task, index = 0) {
  if (!task || typeof task !== 'object') return null;
  const title = String(task.title || '').trim();
  const prompt = String(task.prompt || '').trim();
  if (!title || !prompt) return null;

  const files = normalizeTutorialFiles(task);
  return {
    id: String(task.id || `tutorial-${index}`),
    title,
    intro: String(task.intro || task.detail || task.description || '').trim(),
    files,
    prompt,
    requiresDesktop: task.requiresDesktop !== false,
  };
}

export function normalizeTutorialTasks(tasks) {
  const normalized = (Array.isArray(tasks) ? tasks : [])
    .map((task, index) => normalizeTutorialTask(task, index))
    .filter(Boolean);
  return normalized.length > 0 ? normalized : TUTORIAL_TASKS;
}

export function TutorialEmptyState({ tasks = TUTORIAL_TASKS, onSelectTask, onDismiss }) {
  const visibleTasks = useMemo(() => normalizeTutorialTasks(tasks), [tasks]);
  if (visibleTasks.length === 0) return null;

  return (
    <div className="cc-tutorial-empty">
      <div className="cc-tutorial-empty-kicker">试一个文件任务</div>
      <div className="cc-tutorial-empty-desc">下载一份示例文件，让 CatsCo 演示读图和整理本机文件。</div>
      <div className="cc-tutorial-task-grid">
        {visibleTasks.map((task) => (
          <TutorialTaskCard key={task.id} task={task} onClick={() => onSelectTask(task)} />
        ))}
      </div>
      <button type="button" className="cc-tutorial-skip" onClick={onDismiss}>
        暂时不用
      </button>
    </div>
  );
}

export function TutorialTaskPicker({ tasks = TUTORIAL_TASKS, onClose, onSelectTask }) {
  const visibleTasks = useMemo(() => normalizeTutorialTasks(tasks), [tasks]);

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div className="oc-modal catsco-download-modal cc-tutorial-modal" onClick={(event) => event.stopPropagation()}>
        <div className="oc-modal-header catsco-download-header">
          <div>
            <h3>选择示例任务</h3>
            <p>选择一个任务，下载示例文件，然后把准备好的任务说明填入输入栏。</p>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭">
            <X size={18} />
          </button>
        </div>
        <div className="cc-tutorial-task-list">
          {visibleTasks.map((task) => (
            <TutorialTaskCard key={task.id} task={task} onClick={() => onSelectTask(task)} compact />
          ))}
        </div>
      </div>
    </div>
  );
}

export function TutorialTaskModal({
  task,
  desktopReady,
  onClose,
  onBack,
  onApplyPrompt,
  onOpenDesktopConnect,
}) {
  const normalizedTask = useMemo(() => normalizeTutorialTask(task), [task]);
  const [downloadStarted, setDownloadStarted] = useState({});
  if (!normalizedTask) return null;
  const needsDesktop = normalizedTask.requiresDesktop && !desktopReady;

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div className="oc-modal catsco-download-modal cc-tutorial-modal" onClick={(event) => event.stopPropagation()}>
        <div className="oc-modal-header catsco-download-header">
          <div>
            <h3>{normalizedTask.title}</h3>
            {normalizedTask.intro && <p>{normalizedTask.intro}</p>}
          </div>
          <button type="button" onClick={onClose} aria-label="关闭">
            <X size={18} />
          </button>
        </div>

        <div className="cc-tutorial-detail">
          {normalizedTask.files.length > 0 && (
            <div className="cc-tutorial-file-list">
              {normalizedTask.files.map((file, index) => {
                const key = `${file.url}:${index}`;
                return (
                  <div className="catsco-download-card" key={key}>
                    <span className="catsco-download-icon">
                      <FileText size={20} />
                    </span>
                    <span className="catsco-download-copy">
                      <span className="catsco-download-title">{file.name}</span>
                      <span className="catsco-download-desc">示例文件会下载到浏览器默认下载文件夹。</span>
                      {downloadStarted[key] && (
                        <span className="catsco-download-meta">已开始下载。下载完成后点击“填入任务”。</span>
                      )}
                    </span>
                    <PwaDownloadLink
                      className="catsco-download-action"
                      href={file.url}
                      download={file.name}
                      onDownloadStart={() => setDownloadStarted((prev) => ({ ...prev, [key]: true }))}
                      title="下载示例文件"
                    >
                      <Download size={16} />
                    </PwaDownloadLink>
                  </div>
                );
              })}
            </div>
          )}

          <div className="cc-tutorial-prompt">
            <div className="cc-tutorial-prompt-title">将填入输入栏的任务说明</div>
            <div className="cc-tutorial-prompt-body">{normalizedTask.prompt}</div>
          </div>

          <div className="cc-tutorial-actions">
            <button type="button" className="oc-btn oc-btn-default" onClick={onBack}>
              继续试另一个任务
            </button>
            <button
              type="button"
              className="oc-btn oc-btn-primary"
              onClick={() => {
                if (needsDesktop) {
                  onOpenDesktopConnect?.();
                  return;
                }
                onApplyPrompt(normalizedTask.prompt);
              }}
            >
              {needsDesktop ? '先连接 CatsCo' : '填入任务'}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function normalizeTutorialFiles(task) {
  if (Array.isArray(task.files)) {
    return task.files
      .map((file) => ({
        name: String(file?.name || '').trim(),
        url: String(file?.url || '').trim(),
      }))
      .filter((file) => file.name && file.url);
  }

  const name = String(task.mediaName || '').trim();
  const url = String(task.mediaUrl || '').trim();
  return name && url ? [{ name, url }] : [];
}

function TutorialTaskCard({ task, onClick, compact = false }) {
  return (
    <button type="button" className={`cc-tutorial-card${compact ? ' compact' : ''}`} onClick={onClick}>
      <span className="cc-tutorial-card-icon">
        <FileText size={18} />
      </span>
      <span className="cc-tutorial-card-copy">
        <span className="cc-tutorial-card-title">{task.title}</span>
        {task.intro && <span className="cc-tutorial-card-desc">{task.intro}</span>}
      </span>
      <span className="cc-tutorial-card-arrow">
        <ChevronRight size={16} />
      </span>
    </button>
  );
}
