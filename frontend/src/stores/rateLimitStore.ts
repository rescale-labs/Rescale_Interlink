import { create } from 'zustand';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import type { LogEventDTO } from '../types/events';

// How long the footer indicator lingers after the last rate-limit event
// before clearing. Rate-limit events arrive in bursts with gaps between them;
// a generous window keeps the indicator steady across a burst instead of
// flickering in and out. Each new event slides the window forward.
const CLEAR_AFTER_MS = 20000;

interface RateLimitState {
  active: boolean;
  setupEventListeners: () => () => void;
}

export const useRateLimitStore = create<RateLimitState>((set) => {
  let clearTimer: ReturnType<typeof setTimeout> | undefined;

  return {
    active: false,

    setupEventListeners: () => {
      const handleLog = (event: LogEventDTO) => {
        if (event.stage !== 'rate-limit') return;

        set({ active: true });

        if (clearTimer !== undefined) clearTimeout(clearTimer);
        clearTimer = setTimeout(() => {
          set({ active: false });
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
