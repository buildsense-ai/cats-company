import { useEffect, useState } from 'react';
import { getToken } from '../api';
import { canUsePush } from '../utils/push-notifications';
import { enqueuePushOperation } from '../utils/push-operation';
import { retryPendingPushUnsubscribe } from '../utils/push-session-cleanup';
import { pushTabCoordinator } from '../utils/push-tab-coordination';

const PUSH_CLEANUP_RETRY_DELAY_MS = 30_000;

export default function PushCleanupController() {
  const [online, setOnline] = useState(() => navigator.onLine !== false);
  const [retryVersion, setRetryVersion] = useState(0);

  useEffect(() => {
    const handleOnline = () => setOnline(true);
    const handleOffline = () => setOnline(false);
    window.addEventListener('online', handleOnline);
    window.addEventListener('offline', handleOffline);
    return () => {
      window.removeEventListener('online', handleOnline);
      window.removeEventListener('offline', handleOffline);
    };
  }, []);

  useEffect(() => {
    if (!online || !canUsePush()) return undefined;

    let cancelled = false;
    let retryTimer;
    const retryPendingCleanup = async () => {
      const cleaned = await retryPendingPushUnsubscribe({
        coordinator: pushTabCoordinator,
        isLoggedOut: () => !getToken(),
      });
      if (!cleaned && !cancelled && !getToken()) {
        retryTimer = window.setTimeout(() => {
          setRetryVersion((current) => current + 1);
        }, PUSH_CLEANUP_RETRY_DELAY_MS);
      }
    };

    enqueuePushOperation(retryPendingCleanup).catch((error) => {
      console.warn('Pending push cleanup retry failed:', error);
    });

    return () => {
      cancelled = true;
      window.clearTimeout(retryTimer);
    };
  }, [online, retryVersion]);

  return null;
}
