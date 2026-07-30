import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { Check, ChevronDown } from 'lucide-react';

const FLOATING_GAP = 4;
const VIEWPORT_GUTTER = 8;
const MAX_OPTIONS_HEIGHT = 240;

export default function CustomSelect({
  ariaLabel,
  children,
  className = '',
  disabled = false,
  onValueChange,
  placement = 'bottom',
  value,
}) {
  const rootRef = useRef(null);
  const triggerRef = useRef(null);
  const listRef = useRef(null);
  const listboxID = React.useId();
  const options = React.Children.toArray(children).map((child, index) => ({
    disabled: Boolean(child.props.disabled),
    id: `${listboxID}-option-${index}`,
    key: child.key || `${child.props.value}-${index}`,
    label: child.props.children,
    value: String(child.props.value ?? ''),
  }));
  const firstEnabledIndex = options.findIndex((option) => !option.disabled);
  const matchedIndex = options.findIndex((option) => option.value === String(value));
  const selectedIndex = matchedIndex >= 0 ? matchedIndex : Math.max(0, firstEnabledIndex);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(selectedIndex);
  const [floatingStyle, setFloatingStyle] = useState(null);

  const enabledIndexFrom = (startIndex, direction, includeStart = false) => {
    if (options.length === 0) return -1;
    for (let offset = includeStart ? 0 : 1; offset <= options.length; offset += 1) {
      const index = (startIndex + (offset * direction) + options.length) % options.length;
      if (!options[index]?.disabled) return index;
    }
    return -1;
  };

  useEffect(() => {
    if (!open) return undefined;
    const closeOnOutsidePointer = (event) => {
      if (
        !rootRef.current?.contains(event.target)
        && !listRef.current?.contains(event.target)
      ) {
        setOpen(false);
      }
    };
    document.addEventListener('pointerdown', closeOnOutsidePointer);
    listRef.current?.focus();
    const focusFrame = window.requestAnimationFrame(() => listRef.current?.focus());
    return () => {
      window.cancelAnimationFrame(focusFrame);
      document.removeEventListener('pointerdown', closeOnOutsidePointer);
    };
  }, [open]);

  const openList = (nextIndex = selectedIndex) => {
    if (disabled) return;
    const enabledIndex = options[nextIndex]?.disabled
      ? enabledIndexFrom(nextIndex, 1, true)
      : nextIndex;
    setActiveIndex(enabledIndex >= 0 ? enabledIndex : 0);
    setOpen(true);
  };

  const closeList = ({ restoreFocus = false } = {}) => {
    setOpen(false);
    setFloatingStyle(null);
    if (restoreFocus) triggerRef.current?.focus();
  };

  const chooseOption = (index) => {
    const option = options[index];
    if (!option || option.disabled) return;
    onValueChange(option.value);
    closeList({ restoreFocus: true });
  };

  const moveActiveOption = (direction) => {
    const nextIndex = enabledIndexFrom(activeIndex, direction);
    if (nextIndex >= 0) setActiveIndex(nextIndex);
  };

  const handleTriggerKeyDown = (event) => {
    if (disabled || (event.key !== 'ArrowDown' && event.key !== 'ArrowUp')) return;
    event.preventDefault();
    const direction = event.key === 'ArrowDown' ? 1 : -1;
    const nextIndex = enabledIndexFrom(selectedIndex, direction);
    openList(nextIndex);
  };

  const handleListKeyDown = (event) => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      moveActiveOption(event.key === 'ArrowDown' ? 1 : -1);
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault();
      const edgeIndex = event.key === 'Home'
        ? enabledIndexFrom(0, 1, true)
        : enabledIndexFrom(options.length - 1, -1, true);
      if (edgeIndex >= 0) setActiveIndex(edgeIndex);
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      chooseOption(activeIndex);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      closeList({ restoreFocus: true });
    } else if (event.key === 'Tab') {
      closeList({ restoreFocus: true });
    }
  };

  const updateFloatingPosition = useCallback(() => {
    const trigger = triggerRef.current;
    const list = listRef.current;
    if (!open || !trigger || !list) return;

    const triggerRect = trigger.getBoundingClientRect();
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;
    const availableAbove = triggerRect.top - VIEWPORT_GUTTER - FLOATING_GAP;
    const availableBelow = viewportHeight - triggerRect.bottom - VIEWPORT_GUTTER - FLOATING_GAP;
    const measuredHeight = Math.min(list.scrollHeight || MAX_OPTIONS_HEIGHT, MAX_OPTIONS_HEIGHT);
    const minimumUsefulHeight = Math.min(measuredHeight, 120);
    let opensAbove = placement === 'top';

    if (opensAbove && availableAbove < minimumUsefulHeight && availableBelow > availableAbove) {
      opensAbove = false;
    } else if (!opensAbove && availableBelow < minimumUsefulHeight && availableAbove > availableBelow) {
      opensAbove = true;
    }

    const availableHeight = Math.max(
      48,
      Math.floor(opensAbove ? availableAbove : availableBelow),
    );
    const maxHeight = Math.min(MAX_OPTIONS_HEIGHT, availableHeight);
    const renderedHeight = Math.min(list.scrollHeight || maxHeight, maxHeight);
    const width = Math.min(triggerRect.width, viewportWidth - (VIEWPORT_GUTTER * 2));
    const left = Math.min(
      Math.max(triggerRect.left, VIEWPORT_GUTTER),
      viewportWidth - VIEWPORT_GUTTER - width,
    );
    const top = opensAbove
      ? Math.max(VIEWPORT_GUTTER, triggerRect.top - FLOATING_GAP - renderedHeight)
      : Math.min(
        viewportHeight - VIEWPORT_GUTTER - renderedHeight,
        triggerRect.bottom + FLOATING_GAP,
      );

    setFloatingStyle({
      left,
      maxHeight,
      position: 'fixed',
      top,
      visibility: 'visible',
      width,
    });
  }, [open, placement]);

  useLayoutEffect(() => {
    if (!open) return undefined;
    updateFloatingPosition();
    window.addEventListener('resize', updateFloatingPosition);
    window.addEventListener('scroll', updateFloatingPosition, true);
    return () => {
      window.removeEventListener('resize', updateFloatingPosition);
      window.removeEventListener('scroll', updateFloatingPosition, true);
    };
  }, [open, updateFloatingPosition]);

  const selectedOption = options[selectedIndex];
  const optionList = open && createPortal(
    <div
      ref={listRef}
      id={listboxID}
      className="v3-custom-model-select-options is-portal"
      role="listbox"
      aria-label={ariaLabel}
      aria-activedescendant={options[activeIndex]?.id}
      tabIndex={-1}
      style={floatingStyle || {
        left: 0,
        maxHeight: MAX_OPTIONS_HEIGHT,
        position: 'fixed',
        top: 0,
        visibility: 'hidden',
        width: 0,
      }}
      onKeyDown={handleListKeyDown}
    >
      {options.map((option, index) => (
        <button
          type="button"
          id={option.id}
          key={option.key}
          className={`v3-custom-model-select-option ${index === activeIndex ? 'is-active' : ''}`}
          role="option"
          aria-selected={option.value === String(value)}
          disabled={option.disabled}
          tabIndex={-1}
          onMouseEnter={() => {
            if (!option.disabled) setActiveIndex(index);
          }}
          onClick={() => chooseOption(index)}
        >
          {option.label}
          {option.value === String(value) && <Check size={14} aria-hidden="true" />}
        </button>
      ))}
    </div>,
    document.body,
  );

  return (
    <span
      ref={rootRef}
      className={`v3-custom-model-select-wrap is-${placement} ${className}`.trim()}
    >
      <button
        ref={triggerRef}
        type="button"
        className="v3-custom-model-select-trigger"
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listboxID : undefined}
        data-value={String(value)}
        disabled={disabled}
        onClick={() => open ? closeList() : openList()}
        onKeyDown={handleTriggerKeyDown}
      >
        <span>{selectedOption?.label}</span>
        <ChevronDown className="v3-custom-model-select-chevron" size={16} aria-hidden="true" />
      </button>
      {optionList}
    </span>
  );
}
