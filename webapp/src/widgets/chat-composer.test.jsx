import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import ChatComposer, { CHAT_COMPOSER_HINT, voiceWavePhaseSpeed } from './chat-composer';

describe('ChatComposer', () => {
	it('eases the waveform into its steady movement speed', () => {
		expect(voiceWavePhaseSpeed(100)).toBeLessThan(voiceWavePhaseSpeed(1100));
		expect(voiceWavePhaseSpeed(1100)).toBeLessThan(voiceWavePhaseSpeed(2200));
		expect(voiceWavePhaseSpeed(2200)).toBeCloseTo(1.8);
		expect(voiceWavePhaseSpeed(4400)).toBeCloseTo(1.8);
	});

	it('keeps unsupported voice input visible as a disabled control', async () => {
		await act(async () => {
			root.render(
				<ChatComposer
					value=""
					onChange={() => {}}
					onSend={() => {}}
					onVoiceFinal={() => {}}
					voiceInputAvailable={false}
				/>,
			);
		});

		const button = container.querySelector('button[aria-label="当前浏览器不支持语音输入"]');
		expect(button).not.toBeNull();
		expect(button.disabled).toBe(true);
	});

  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn().mockReturnValue({ matches: false }),
    });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    delete window.matchMedia;
    vi.clearAllMocks();
  });

  async function renderComposer(extraProps = {}) {
    await act(async () => {
      root.render(
        <ChatComposer
          value=""
          placeholder="输入指令，我帮您完成"
          onChange={vi.fn()}
          onAttachmentToggle={vi.fn()}
          onSend={vi.fn()}
          {...extraProps}
        />,
      );
    });
  }

  function createTouchEvent(type, changedTouches) {
    const event = new Event(type, { bubbles: true, cancelable: true });
    Object.defineProperty(event, 'changedTouches', {
      configurable: true,
      value: changedTouches,
    });
    return event;
  }

  it('renders the shared control row and keeps the hint outside the pill', async () => {
    await renderComposer();

    const composer = container.querySelector('.v3-composer');
    const box = composer.querySelector('.v3-composer-box');
    const row = box.querySelector('.v3-composer-row');
    const hint = composer.querySelector('.v3-composer-hint');

    expect(row.querySelector('.v3-attachment-picker > button.v3-composer-plus')).not.toBeNull();
    expect(row.querySelector('textarea.v3-composer-input')).not.toBeNull();
    expect(row.querySelector('.v3-agent-picker')).toBeNull();
    expect(row.querySelector('button.v3-send')).not.toBeNull();
    expect(hint.textContent).toBe(CHAT_COMPOSER_HINT);
    expect(hint.parentElement).toBe(composer);
    expect(box.contains(hint)).toBe(false);
  });

  it('renders the optional Agent selector only when a caller provides it', async () => {
    const onAgentToggle = vi.fn();
    await renderComposer({
      agentName: '代码审查助手',
      agentOpen: true,
      onAgentToggle,
      agentMenu: <div className="test-agent-menu">Agent 菜单</div>,
    });

    const agentButton = container.querySelector('.v3-agent-picker-button');
    expect(agentButton?.textContent).toContain('代码审查助手');
    expect(container.querySelector('.test-agent-menu')).not.toBeNull();

    await act(async () => {
      agentButton.click();
    });
    expect(onAgentToggle).toHaveBeenCalledTimes(1);
  });

  it('forwards mention accessibility attributes to the textarea', async () => {
    await renderComposer({
      textareaProps: {
        'aria-controls': 'mention-picker',
        'aria-expanded': true,
        'aria-activedescendant': 'mention-option-42',
      },
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    expect(textarea.getAttribute('aria-controls')).toBe('mention-picker');
    expect(textarea.getAttribute('aria-expanded')).toBe('true');
    expect(textarea.getAttribute('aria-activedescendant')).toBe('mention-option-42');
    expect(textarea.getAttribute('aria-label')).toBe('输入指令，我帮您完成');
    expect(textarea.getAttribute('name')).toBe('message');
    expect(textarea.getAttribute('autocomplete')).toBe('off');
  });

  it('keeps reply context and live notices inside the shared composer surface', async () => {
    await renderComposer({
      context: <div className="test-context">回复：上一条消息</div>,
      notices: <div className="v3-live-input-status">CatsCo 正在处理</div>,
    });

    const box = container.querySelector('.v3-composer-box');
    const context = box.querySelector('.v3-composer-context');
    const notices = box.querySelector('.v3-composer-notices');
    const row = box.querySelector('.v3-composer-row');
    expect(box.classList.contains('has-context')).toBe(true);
    expect(box.classList.contains('has-notices')).toBe(true);
    expect(context?.textContent).toContain('上一条消息');
    expect(notices?.textContent).toContain('正在处理');
    expect(context.nextElementSibling).toBe(notices);
    expect(notices.nextElementSibling).toBe(row);
  });

  it('marks the composer busy while an Agent reply is in progress', async () => {
    await renderComposer({ agentReplyActive: true });

    const box = container.querySelector('.v3-composer-box');
    expect(box.classList.contains('is-agent-reply-active')).toBe(true);
    expect(box.getAttribute('aria-busy')).toBe('true');

    await renderComposer({ agentReplyActive: false });
    expect(box.classList.contains('is-agent-reply-active')).toBe(false);
    expect(box.getAttribute('aria-busy')).toBe('false');
  });

  it('uses the same action slot for stop while working and send after input', async () => {
    const onSend = vi.fn();
    const onStop = vi.fn();
    await renderComposer({ value: '', stop: true, onSend, onStop });

    const stopButton = container.querySelector('button[aria-label="停止当前工作"]');
    expect(stopButton).not.toBeNull();

    await act(async () => {
      stopButton.click();
    });
    expect(onStop).toHaveBeenCalledTimes(1);

    await renderComposer({ value: '追加消息', stop: false, onSend, onStop });
    const sendButton = container.querySelector('button[aria-label="发送"]');
    expect(sendButton).not.toBeNull();
    await act(async () => {
      sendButton.click();
    });
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it('keeps streaming partial text local and commits only the final transcript', async () => {
	const onVoiceFinal = vi.fn();
	let callbacks;
	const voiceSession = {
		prepare: vi.fn().mockResolvedValue(undefined),
		start: vi.fn().mockResolvedValue(undefined),
		stop: vi.fn(),
		cancel: vi.fn(),
	};
	await renderComposer({
		onVoiceFinal,
		voiceInputAvailable: true,
		createVoiceSession: (options) => {
			callbacks = options;
			return voiceSession;
		},
	});

	const startButton = container.querySelector('button[aria-label="开始语音输入"]');
	expect(startButton).not.toBeNull();
	await act(async () => {
		startButton.click();
		await Promise.resolve();
	});

	const textarea = container.querySelector('textarea.v3-composer-input');
	Object.defineProperty(textarea, 'scrollHeight', { configurable: true, value: 320 });
	await act(async () => {
		callbacks.onState('recording');
		callbacks.onPartial('正在识别的文字');
	});
	expect(textarea.value).toBe('正在识别的文字');
	expect(textarea.scrollTop).toBe(320);
	expect(container.querySelector('.v3-voice-status')).toBeNull();
	expect(onVoiceFinal).not.toHaveBeenCalled();

	await act(async () => {
		container.querySelector('button[aria-label="停止语音输入"]').click();
	});
	expect(voiceSession.stop).toHaveBeenCalledTimes(1);

	await act(async () => {
		callbacks.onFinal('最终文字');
	});
	expect(onVoiceFinal).toHaveBeenCalledWith('最终文字', expect.objectContaining({
		baseValue: '',
		start: 0,
		end: 0,
	}));
  });

  it('uses a calm limit notice and confirms the draft was preserved', async () => {
    let callbacks;
    const onVoiceFinal = vi.fn();
    const voiceSession = {
      prepare: vi.fn().mockResolvedValue(undefined),
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn(),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal,
      voiceInputAvailable: true,
      createVoiceSession: (options) => {
        callbacks = options;
        return voiceSession;
      },
    });

    await act(async () => {
      container.querySelector('button[aria-label="开始语音输入"]').click();
      await Promise.resolve();
      callbacks.onState('recording');
      callbacks.onDurationWarning({ remainingMs: 10_000, hasRecentInput: true });
    });

    const hint = container.querySelector('.v3-composer-hint');
    expect(hint.textContent).toContain('当前内容会自动保留');
    expect(hint.classList.contains('is-error')).toBe(false);

    await act(async () => {
      callbacks.onDurationLimit({ hadRecentInput: true, stoppedAtNaturalBoundary: false });
      callbacks.onFinal('已保存的长语音', { reason: 'duration_limit' });
    });

    expect(onVoiceFinal).toHaveBeenCalledWith(
      '已保存的长语音',
      expect.objectContaining({ baseValue: '' }),
    );
    expect(container.querySelector('.v3-composer-hint')?.textContent)
      .toContain('已保留本段语音');
  });

  it('shows a calm notice for a successfully finalized idle boundary', async () => {
    let callbacks;
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: (options) => {
        callbacks = options;
        return {
          prepare: vi.fn().mockResolvedValue(undefined),
          start: vi.fn().mockResolvedValue(undefined),
          stop: vi.fn(),
          cancel: vi.fn(),
        };
      },
    });

    await act(async () => {
      container.querySelector('button[aria-label="开始语音输入"]').click();
      await Promise.resolve();
      callbacks.onState('recording');
      callbacks.onFinal('静音前的内容', { reason: 'idle_timeout' });
    });

    expect(container.querySelector('.v3-composer-hint')?.textContent)
      .toContain('已保留本段语音');
    expect(container.querySelector('.v3-composer-hint')?.classList.contains('is-error')).toBe(false);
  });

  it('keeps idle-boundary finalization failures in the error state', async () => {
    let callbacks;
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: (options) => {
        callbacks = options;
        return {
          prepare: vi.fn().mockResolvedValue(undefined),
          start: vi.fn().mockResolvedValue(undefined),
          stop: vi.fn(),
          cancel: vi.fn(),
        };
      },
    });

    await act(async () => {
      container.querySelector('button[aria-label="开始语音输入"]').click();
      await Promise.resolve();
      callbacks.onState('finalizing');
      callbacks.onError(
        new Error('语音识别结束超时，请重试'),
        '静音前的内容',
        { reason: 'idle_timeout' },
      );
    });

    const hint = container.querySelector('.v3-composer-hint');
    expect(hint?.textContent).toContain('语音识别结束超时，请重试');
    expect(hint?.classList.contains('is-error')).toBe(true);
  });

  it('allows another voice session immediately after a terminal recognition error', async () => {
    const sessions = [];
    const callbacks = [];
    const createVoiceSession = vi.fn((options) => {
      callbacks.push(options);
      const session = {
        prepare: vi.fn().mockResolvedValue(undefined),
        start: vi.fn().mockResolvedValue(undefined),
        stop: vi.fn(),
        cancel: vi.fn(),
      };
      sessions.push(session);
      return session;
    });
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession,
    });

    await act(async () => {
      container.querySelector('button[aria-label="开始语音输入"]').click();
      await Promise.resolve();
      callbacks[0].onState('finalizing');
      callbacks[0].onError(new Error('语音识别结束超时，请重试'));
    });

    expect(container.querySelector('.v3-composer-hint')?.textContent).toContain('语音识别结束超时，请重试');
    expect(container.querySelector('button[aria-label="开始语音输入"]')).not.toBeNull();

    await act(async () => {
      container.querySelector('button[aria-label="开始语音输入"]').click();
      await Promise.resolve();
    });

    expect(createVoiceSession).toHaveBeenCalledTimes(2);
    expect(sessions[1].start).toHaveBeenCalledTimes(1);
  });

  it('keeps the latest recognized text in the draft when recognition times out', async () => {
    let callbacks;
    const onVoiceFinal = vi.fn();
    const voiceSession = {
      prepare: vi.fn().mockResolvedValue(undefined),
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn(),
      cancel: vi.fn(),
    };
    await renderComposer({
      value: '已有草稿：',
      onVoiceFinal,
      voiceInputAvailable: true,
      createVoiceSession: (options) => {
        callbacks = options;
        return voiceSession;
      },
    });

    await act(async () => {
      container.querySelector('button[aria-label="开始语音输入"]').click();
      await Promise.resolve();
      callbacks.onState('recording');
      callbacks.onPartial('补充的长语音');
      callbacks.onError(new Error('语音识别结束超时，请重试'), '补充的长语音');
    });

    expect(onVoiceFinal).toHaveBeenCalledWith(
      '补充的长语音',
      expect.objectContaining({ baseValue: '已有草稿：' }),
    );
    expect(container.querySelector('.v3-composer-hint')?.textContent)
      .toContain('语音识别结束超时，请重试');
  });

  it('begins microphone pre-roll on touch down before the long-hold threshold', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      prepare: vi.fn().mockResolvedValue(undefined),
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn(),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 31,
        pointerType: 'touch',
        clientY: 420,
      }));
      await Promise.resolve();
    });
    expect(voiceSession.prepare).toHaveBeenCalledTimes(1);
    expect(voiceSession.start).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(279);
      await Promise.resolve();
    });
    expect(voiceSession.start).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(1);
      await Promise.resolve();
    });
    expect(voiceSession.start).toHaveBeenCalledTimes(1);

    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 31,
        pointerType: 'touch',
        clientY: 420,
      }));
    });
    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('reuses touch pre-roll when a short tap starts normal voice input', async () => {
    vi.useFakeTimers();
    const sessions = [];
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => {
        const session = {
          prepare: vi.fn().mockResolvedValue(undefined),
          start: vi.fn().mockResolvedValue(undefined),
          stop: vi.fn(),
          cancel: vi.fn(),
        };
        sessions.push(session);
        return session;
      },
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 32,
        pointerType: 'touch',
        clientY: 420,
      }));
      voiceButton.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 32,
        pointerType: 'touch',
        clientY: 420,
      }));
      await Promise.resolve();
    });

    expect(sessions).toHaveLength(1);
    expect(sessions[0].prepare).toHaveBeenCalledTimes(1);
    expect(sessions[0].cancel).not.toHaveBeenCalled();
    expect(sessions[0].start).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('cancels an unactivated touch pre-roll when the gesture is interrupted', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      prepare: vi.fn().mockResolvedValue(undefined),
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn(),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 33,
        pointerType: 'touch',
        clientY: 420,
      }));
      voiceButton.dispatchEvent(new PointerEvent('pointercancel', {
        bubbles: true,
        pointerId: 33,
        pointerType: 'touch',
        clientY: 420,
      }));
      await Promise.resolve();
    });

    expect(voiceSession.cancel).toHaveBeenCalledTimes(1);
    expect(voiceSession.start).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it('shows the mobile hold overlay and stops recognition when touch is released', async () => {
    vi.useFakeTimers();
    let callbacks;
    let composerRenders = 0;
    const rootProps = {};
    Object.defineProperty(rootProps, 'data-render-probe', {
      enumerable: true,
      get() {
        composerRenders += 1;
        return composerRenders;
      },
    });
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: (options) => {
        callbacks = options;
        return voiceSession;
      },
      rootProps,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      React.act(() => {
        voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
          bubbles: true,
          pointerId: 7,
          pointerType: 'touch',
          clientY: 420,
        }));
      });
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    expect(voiceSession.start).toHaveBeenCalledTimes(1);
    expect(container.querySelector('.v3-voice-hold-overlay')).not.toBeNull();
    expect(container.querySelector('textarea.v3-composer-input')).not.toBeNull();

    const transcript = container.querySelector('.v3-voice-hold-transcript');
    Object.defineProperty(transcript, 'scrollHeight', { configurable: true, value: 180 });
    const initialWavePath = container.querySelector('.v3-voice-hold-wave-fill').getAttribute('d');
    await act(async () => {
      callbacks.onState('recording');
      callbacks.onPartial('这是很长的实时转录文字，新的内容应该始终保持可见');
    });
    const rendersBeforeWave = composerRenders;
    await act(async () => {
      callbacks.onAudioLevel(0.78);
      vi.advanceTimersByTime(34);
    });
    expect(transcript.textContent).toContain('新的内容应该始终保持可见');
    expect(transcript.scrollTop).toBe(180);
    expect(composerRenders).toBe(rendersBeforeWave);
    expect(container.querySelector('.v3-voice-hold-wave-fill').getAttribute('d'))
      .not.toBe(initialWavePath);
    const liveRegions = container.querySelectorAll('[role="status"]');
    expect(liveRegions).toHaveLength(1);
    expect(liveRegions[0].textContent).toBe('正在听…');

    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 7,
        pointerType: 'touch',
        clientY: 420,
      }));
      await Promise.resolve();
    });

    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    expect(voiceSession.cancel).not.toHaveBeenCalled();
    expect(container.querySelector('.v3-voice-hold-overlay')).toBeNull();
    vi.useRealTimers();
  });

  it('finishes a mobile hold when the release lands on document after capture is lost', async () => {
    vi.useFakeTimers();
    const onVoiceFinal = vi.fn();
    let callbacks;
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal,
      voiceInputAvailable: true,
      createVoiceSession: (options) => {
        callbacks = options;
        return voiceSession;
      },
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 21,
        pointerType: 'touch',
        clientY: 420,
      }));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    await act(async () => {
      document.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 21,
        pointerType: 'touch',
        clientY: 420,
      }));
      await Promise.resolve();
    });

    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    expect(voiceSession.cancel).not.toHaveBeenCalled();

    await act(async () => {
      callbacks.onFinal('移动端最终文字');
    });
    expect(onVoiceFinal).toHaveBeenCalledWith('移动端最终文字', expect.objectContaining({
      baseValue: '',
      start: 0,
      end: 0,
    }));
    vi.useRealTimers();
  });

  it('keeps a mobile hold active after capture is lost until its pointer is released', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 24,
        pointerType: 'touch',
        clientY: 420,
      }));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('lostpointercapture', {
        bubbles: true,
        pointerId: 24,
        pointerType: 'touch',
      }));
      await Promise.resolve();
    });

    expect(voiceSession.stop).not.toHaveBeenCalled();
    expect(container.querySelector('.v3-voice-hold-overlay')).not.toBeNull();

    await act(async () => {
      document.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 24,
        pointerType: 'touch',
        clientY: 420,
      }));
      await Promise.resolve();
    });

    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('does not turn capture loss before the hold threshold into a voice click', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 27,
        pointerType: 'touch',
        clientY: 420,
      }));
      await Promise.resolve();
      voiceButton.dispatchEvent(new PointerEvent('lostpointercapture', {
        bubbles: true,
        pointerId: 27,
        pointerType: 'touch',
      }));
      await Promise.resolve();
    });

    expect(voiceSession.start).not.toHaveBeenCalled();
    expect(voiceSession.stop).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it('ignores an unrelated touch ending while a mobile hold remains active', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    const voiceTouch = {
      identifier: 51,
      clientX: 24,
      clientY: 420,
      target: voiceButton,
    };
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 25,
        pointerType: 'touch',
        clientX: 24,
        clientY: 420,
      }));
      voiceButton.dispatchEvent(createTouchEvent('touchstart', [voiceTouch]));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    await act(async () => {
      document.dispatchEvent(createTouchEvent('touchend', [{
        identifier: 52,
        clientX: 200,
        clientY: 420,
        target: document.body,
      }]));
      await Promise.resolve();
    });

    expect(voiceSession.stop).not.toHaveBeenCalled();
    expect(container.querySelector('.v3-voice-hold-overlay')).not.toBeNull();

    await act(async () => {
      document.dispatchEvent(createTouchEvent('touchend', [voiceTouch]));
      await Promise.resolve();
    });

    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('does not bind an unmatched touchstart to the active mobile hold', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    const unmatchedTouch = {
      identifier: 53,
      clientX: 200,
      clientY: 420,
      target: voiceButton,
    };
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 28,
        pointerType: 'touch',
        clientX: 24,
        clientY: 420,
      }));
      voiceButton.dispatchEvent(createTouchEvent('touchstart', [unmatchedTouch]));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
      document.dispatchEvent(createTouchEvent('touchend', [unmatchedTouch]));
      await Promise.resolve();
    });

    expect(voiceSession.stop).not.toHaveBeenCalled();

    await act(async () => {
      document.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 28,
        pointerType: 'touch',
        clientX: 24,
        clientY: 420,
      }));
      await Promise.resolve();
    });

    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('matches the touch fallback when touchstart precedes pointerdown', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    const voiceTouch = {
      identifier: 61,
      clientX: 24,
      clientY: 420,
      target: voiceButton,
    };
    await act(async () => {
      voiceButton.dispatchEvent(createTouchEvent('touchstart', [voiceTouch]));
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 26,
        pointerType: 'touch',
        clientX: 24,
        clientY: 420,
      }));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
      document.dispatchEvent(createTouchEvent('touchend', [voiceTouch]));
      await Promise.resolve();
    });

    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('does not reuse a touch from stopping normal voice input for the next hold', async () => {
    vi.useFakeTimers();
    const callbacks = [];
    const sessions = [];
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: (options) => {
        callbacks.push(options);
        const session = {
          start: vi.fn().mockResolvedValue(undefined),
          stop: vi.fn().mockResolvedValue(undefined),
          cancel: vi.fn(),
        };
        sessions.push(session);
        return session;
      },
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.click();
      await Promise.resolve();
      callbacks[0].onState('recording');
    });

    const priorTouch = {
      identifier: 71,
      clientX: 24,
      clientY: 420,
      target: voiceButton,
    };
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 29,
        pointerType: 'touch',
        clientX: 24,
        clientY: 420,
      }));
      voiceButton.dispatchEvent(createTouchEvent('touchstart', [priorTouch]));
      voiceButton.click();
      await Promise.resolve();
      callbacks[0].onFinal('第一轮完成');
    });

    const nextTouch = {
      identifier: 72,
      clientX: 24,
      clientY: 420,
      target: voiceButton,
    };
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 30,
        pointerType: 'touch',
        clientX: 24,
        clientY: 420,
      }));
      voiceButton.dispatchEvent(createTouchEvent('touchstart', [nextTouch]));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
      document.dispatchEvent(createTouchEvent('touchend', [nextTouch]));
      await Promise.resolve();
    });

    expect(sessions).toHaveLength(2);
    expect(sessions[1].stop).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('still stops when releasing pointer capture reports that capture is already gone', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    voiceButton.setPointerCapture = vi.fn();
    voiceButton.releasePointerCapture = vi.fn(() => {
      throw new DOMException('No active pointer capture', 'NotFoundError');
    });
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 22,
        pointerType: 'touch',
        clientY: 420,
      }));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
      voiceButton.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 22,
        pointerType: 'touch',
        clientY: 420,
      }));
      await Promise.resolve();
    });

    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('finishes a mobile hold from touchend when no pointerup is delivered', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    const voiceTouch = {
      identifier: 23,
      clientX: 0,
      clientY: 420,
      target: voiceButton,
    };
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 23,
        pointerType: 'touch',
        clientY: 420,
      }));
      voiceButton.dispatchEvent(createTouchEvent('touchstart', [voiceTouch]));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
      document.dispatchEvent(createTouchEvent('touchend', [voiceTouch]));
      await Promise.resolve();
    });

    expect(voiceSession.stop).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('keeps the hold waveform static when reduced motion is requested', async () => {
    vi.useFakeTimers();
    window.matchMedia.mockReturnValue({ matches: true });
    const requestAnimationFrame = vi.spyOn(window, 'requestAnimationFrame');
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn().mockResolvedValue(undefined),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 17,
        pointerType: 'touch',
        clientY: 420,
      }));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-voice-hold-wave')).not.toBeNull();
    expect(requestAnimationFrame).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it('closes a mobile hold overlay when voice startup fails without restarting on release', async () => {
    vi.useFakeTimers();
    let callbacks;
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn(),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: (options) => {
        callbacks = options;
        return voiceSession;
      },
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 12,
        pointerType: 'touch',
        clientY: 420,
      }));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-voice-hold-overlay')).not.toBeNull();

    await act(async () => {
      callbacks.onError(new Error('需要麦克风权限才能使用语音输入'));
    });
    expect(container.querySelector('.v3-voice-hold-overlay')).toBeNull();
    expect(container.querySelector('.v3-composer-hint').textContent).toContain('麦克风权限');

    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 12,
        pointerType: 'touch',
        clientY: 420,
      }));
      voiceButton.click();
      await Promise.resolve();
    });
    expect(voiceSession.start).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });

  it('cancels a mobile hold recording after sliding upward', async () => {
    vi.useFakeTimers();
    const voiceSession = {
      start: vi.fn().mockResolvedValue(undefined),
      stop: vi.fn(),
      cancel: vi.fn(),
    };
    await renderComposer({
      onVoiceFinal: vi.fn(),
      voiceInputAvailable: true,
      createVoiceSession: () => voiceSession,
    });

    const voiceButton = container.querySelector('button[aria-label="开始语音输入"]');
    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        pointerId: 9,
        pointerType: 'touch',
        clientY: 420,
      }));
      vi.advanceTimersByTime(300);
      await Promise.resolve();
      voiceButton.dispatchEvent(new PointerEvent('pointermove', {
        bubbles: true,
        pointerId: 9,
        pointerType: 'touch',
        clientY: 330,
      }));
    });

    expect(container.querySelector('.v3-voice-hold-overlay.is-cancelling')?.textContent).toContain('松开取消输入');

    await act(async () => {
      voiceButton.dispatchEvent(new PointerEvent('pointerup', {
        bubbles: true,
        pointerId: 9,
        pointerType: 'touch',
        clientY: 330,
      }));
    });

    expect(voiceSession.cancel).toHaveBeenCalledTimes(1);
    expect(voiceSession.stop).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it('renders removable image and file attachments inside the expanding composer box', async () => {
    const onRemoveAttachment = vi.fn();
    await renderComposer({
      attachments: [
        {
          type: 'image',
          name: 'cat.jpg',
          content: { payload: { file_key: 'cat', thumbnail: '/uploads/cat.jpg' } },
        },
        {
          type: 'file',
          name: 'brief.pdf',
          content: { payload: { file_key: 'brief' } },
        },
      ],
      onRemoveAttachment,
    });

    const box = container.querySelector('.v3-composer-box.has-attachments');
    expect(box?.querySelectorAll('.v3-composer-attachment-chip')).toHaveLength(2);
    expect(box?.querySelector('.v3-composer-attachment-chip.is-image img')).not.toBeNull();
    expect(box?.textContent).toContain('brief.pdf');

    await act(async () => {
      const previewButton = box.querySelector('button[aria-label="预览图片：cat.jpg"]');
      previewButton.focus();
      previewButton.click();
    });
    expect(container.querySelector('[role="dialog"][aria-label="图片预览：cat.jpg"] img')?.getAttribute('src'))
      .toBe('/uploads/cat.jpg');

    const closePreviewButton = container.querySelector('button[aria-label="关闭图片预览"]');
    expect(document.activeElement).toBe(closePreviewButton);

    const tabEvent = new KeyboardEvent('keydown', {
      key: 'Tab',
      bubbles: true,
      cancelable: true,
    });
    await act(async () => {
      closePreviewButton.dispatchEvent(tabEvent);
    });
    expect(tabEvent.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(closePreviewButton);

    const shiftTabEvent = new KeyboardEvent('keydown', {
      key: 'Tab',
      shiftKey: true,
      bubbles: true,
      cancelable: true,
    });
    await act(async () => {
      closePreviewButton.dispatchEvent(shiftTabEvent);
    });
    expect(shiftTabEvent.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(closePreviewButton);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', {
        key: 'Escape',
        bubbles: true,
        cancelable: true,
      }));
    });
    expect(container.querySelector('[role="dialog"][aria-label="图片预览：cat.jpg"]')).toBeNull();
    expect(document.activeElement).toBe(
      box.querySelector('button[aria-label="预览图片：cat.jpg"]'),
    );

    await act(async () => {
      box.querySelector('button[aria-label="移除附件：cat.jpg"]').click();
    });
    expect(onRemoveAttachment).toHaveBeenCalledWith(0);
  });

  it.each([
    ['attachment', { attachmentOpen: true, attachmentMenu: <div className="test-menu">附件菜单</div> }],
    ['Agent', { agentOpen: true, agentMenu: <div className="test-menu">Agent 菜单</div> }],
  ])('closes the open %s menu only when pointerdown occurs outside', async (_label, menuProps) => {
    const onCloseMenus = vi.fn();
    await renderComposer({ ...menuProps, onCloseMenus });

    const menu = container.querySelector('.test-menu');
    await act(async () => {
      menu.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    });
    expect(onCloseMenus).not.toHaveBeenCalled();

    await act(async () => {
      document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    });
    expect(onCloseMenus).toHaveBeenCalledTimes(1);
  });

  it.each([
    ['attachment', { attachmentOpen: true, attachmentMenu: <div>附件菜单</div> }],
    ['Agent', { agentOpen: true, agentMenu: <div>Agent 菜单</div> }],
  ])('closes the open %s menu when Escape is pressed', async (_label, menuProps) => {
    const onCloseMenus = vi.fn();
    await renderComposer({ ...menuProps, onCloseMenus });

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });

    expect(onCloseMenus).toHaveBeenCalledTimes(1);
  });

  it.each([
    [
      'attachment menu when the textarea is pressed',
      { attachmentOpen: true, attachmentMenu: <div>附件菜单</div> },
      'textarea.v3-composer-input',
    ],
    [
      'Agent menu when the send area is pressed',
      { agentOpen: true, agentMenu: <div>Agent 菜单</div> },
      'button.v3-send',
    ],
  ])('closes the open %s', async (_label, menuProps, targetSelector) => {
    const onCloseMenus = vi.fn();
    await renderComposer({ ...menuProps, onCloseMenus });

    await act(async () => {
      container.querySelector(targetSelector).dispatchEvent(new Event('pointerdown', { bubbles: true }));
    });

    expect(onCloseMenus).toHaveBeenCalledTimes(1);
  });
});
