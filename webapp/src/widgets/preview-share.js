function shareablePreviewURL(url) {
  const value = String(url || '').trim();
  if (!value) return '';

  try {
    const baseURL = typeof window !== 'undefined' && window.location?.href
      ? window.location.href
      : 'http://localhost/';
    const resolvedURL = new URL(value, baseURL);
    if (resolvedURL.searchParams.get('download') === '1') {
      resolvedURL.searchParams.delete('download');
    }
    return resolvedURL.toString();
  } catch {
    return value;
  }
}

export async function sharePreviewLink({ url, name = '文件', navigatorLike = globalThis.navigator } = {}) {
  const shareURL = shareablePreviewURL(url);
  if (!shareURL) return { status: 'error', reason: 'missing-url' };

  if (typeof navigatorLike?.share === 'function') {
    try {
      await navigatorLike.share({
        title: name || '文件',
        url: shareURL,
      });
      return { status: 'shared', method: 'native', url: shareURL };
    } catch (error) {
      if (error?.name === 'AbortError') {
        return { status: 'cancelled', url: shareURL };
      }
      return { status: 'error', reason: 'native-share', error, url: shareURL };
    }
  }

  if (typeof navigatorLike?.clipboard?.writeText === 'function') {
    try {
      await navigatorLike.clipboard.writeText(shareURL);
      return { status: 'copied', method: 'clipboard', url: shareURL };
    } catch (error) {
      return { status: 'error', reason: 'clipboard', error, url: shareURL };
    }
  }

  return { status: 'unsupported', url: shareURL };
}
