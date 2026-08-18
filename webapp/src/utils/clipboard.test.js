import { copyTextToClipboard } from './clipboard';

describe('copyTextToClipboard', () => {
  it('rejects when the legacy copy command cannot write the value', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: undefined,
    });
    Object.defineProperty(document, 'execCommand', {
      configurable: true,
      value: vi.fn(() => false),
    });

    await expect(copyTextToClipboard('share URL')).rejects.toThrow('Copy command failed');
    expect(document.execCommand).toHaveBeenCalledWith('copy');
    expect(document.querySelector('textarea')).toBeNull();
  });
});
