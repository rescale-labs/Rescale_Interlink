import { create } from 'zustand';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import type { LogEventDTO } from '../types/events';

// How long the banner lingers after the last rate-limit event before
// auto-clearing. Each new rate-limit event slides this window forward, so the
// banner stays up for as long as throttling is actively happening and
// disappears shortly after it subsides.
const CLEAR_AFTER_MS = 6000;

interface RateLimitState {
  active: boolean;
  message: string;
  setupEventListeners: () => () => void;
}

export const useRateLimitStore = create<RateLimitState>((set) => {
  let clearTimer: ReturnType<typeof setTimeout> | undefined;

  return {
    active: false,
    message: '',

    setupEventListeners: () => {
      const handleLog = (event: LogEventDTO) => {
        if (event.stage !== 'rate-limit') return;

        set({ active: true, message: event.message });

        if (clearTimer !== undefined) clearTimeout(clearTimer);
        clearTimer = setTimeout(() => {
          set({ active: false, message: '' });
          clearTimer = undefined;
        }, CLEAR_AFTER_MS);
      };

      // Use the unsub callback, never EventsOff (would drop other stores' listeners).
      const unsubLog = EventsOn('interlink:log', handleLog);

      return () => {
        unsubLog();
        if (clearTimer !== undefined) {
          clearTimeout(clearTimer);
          clearTimer = undefined;
        }
      };
    },
  };
});
