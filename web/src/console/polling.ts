export class LatestRequest {
  #controller: AbortController | null = null;
  #revision = 0;
  cancel(): void { this.#revision++; this.#controller?.abort(); }
  async run<T>(read: (signal: AbortSignal) => Promise<T>, apply: (data: T) => void): Promise<void> {
    this.cancel(); const revision = this.#revision;
    const controller = new AbortController(); this.#controller = controller;
    try {
      const value = await read(controller.signal);
      if (!controller.signal.aborted && revision === this.#revision) apply(value);
    } catch (error) { if (!controller.signal.aborted && revision === this.#revision) throw error; }
  }
}

export function pollVisible(document: Document, refresh: () => Promise<void>, cancel: () => void, interval = 5000): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let stopped = false;
  let generation = 0;
  const schedule = () => { if (!stopped && !document.hidden) timer = setTimeout(tick, interval); };
  const tick = async () => { if (stopped || document.hidden) return; const current = generation; try { await refresh(); } finally { if (current === generation) schedule(); } };
  const visibility = () => { generation++; clearTimeout(timer); cancel(); if (!document.hidden) void tick(); };
  document.addEventListener('visibilitychange', visibility);
  void tick();
  return () => { stopped = true; generation++; clearTimeout(timer); cancel(); document.removeEventListener('visibilitychange', visibility); };
}
